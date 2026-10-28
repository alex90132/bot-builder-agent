package mcphub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"bot-builder-agent/internal/agent"
	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/store"
	"bot-builder-agent/internal/text"
)

const (
	clientName    = "bot-builder-agent"
	clientVersion = "0.2.0"
	listTimeout   = 25 * time.Second
	callTimeout   = 60 * time.Second
)

// Hub держит сессии MCP отдельно для каждого чата и сервера.
type Hub struct {
	Store        *store.Store
	BuilderURL   string
	AllowPrivate bool
	PublicURL    string
	CacheTTL     time.Duration

	mu       sync.Mutex
	sessions map[sessKey]*openSession
	cached   map[sessKey]cachedTools
	bind     map[int64]map[string]agent.Remote
	pending  map[string]*oauthPending
}

type sessKey struct {
	user   int64
	server int64
	rev    string
}

type cachedTools struct {
	at    time.Time
	tools []*sdkmcp.Tool
}

// New собирает пул. AllowPrivate по умолчанию включён у вызывающего кода.
func New(st *store.Store, builderURL string, cacheTTL time.Duration) *Hub {
	if cacheTTL < 0 {
		cacheTTL = 0
	}
	return &Hub{
		Store:        st,
		BuilderURL:   builderURL,
		AllowPrivate: true,
		CacheTTL:     cacheTTL,
		sessions:     map[sessKey]*openSession{},
		cached:       map[sessKey]cachedTools{},
		bind:         map[int64]map[string]agent.Remote{},
		pending:      map[string]*oauthPending{},
	}
}

// Catalog собирает инструменты включённых серверов. Сбой одного не отменяет остальные.
func (h *Hub) Catalog(ctx context.Context, userID int64) (agent.RemoteSet, error) {
	servers, err := h.Store.ListServers(ctx, userID)
	if err != nil {
		return agent.RemoteSet{}, err
	}
	type one struct {
		tools []agent.Remote
		fail  string
		build bool
	}
	out := make([]one, len(servers))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, srv := range servers {
		if !srv.Enabled {
			continue
		}
		wg.Add(1)
		go func(i int, srv store.Server) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tools, err := h.listServer(ctx, srv)
			if err != nil {
				out[i].fail = srv.Name + ": " + publicErr(err)
				_ = h.Store.SetServerStatus(ctx, srv.UserID, srv.ID, false, publicErr(err))
				return
			}
			out[i].tools = tools
			out[i].build = srv.Builder
			_ = h.Store.SetServerStatus(ctx, srv.UserID, srv.ID, true, "")
		}(i, srv)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return agent.RemoteSet{}, err
	}
	set := agent.RemoteSet{}
	used := map[string]struct{}{}
	bind := map[string]agent.Remote{}
	for _, item := range out {
		if item.fail != "" {
			set.Failures = append(set.Failures, item.fail)
		}
		if item.build && len(item.tools) > 0 {
			set.HasBuilder = true
		}
		for _, tool := range item.tools {
			tool.PublicName = publicName(tool.ServerName, tool.Original, used)
			bind[tool.PublicName] = tool
			set.Tools = append(set.Tools, tool)
		}
	}
	if len(set.Tools) > 0 && !set.HasBuilder {
		for _, tool := range set.Tools {
			if tool.Builder {
				set.HasBuilder = true
				break
			}
		}
	}
	h.mu.Lock()
	h.bind[userID] = bind
	h.mu.Unlock()
	return set, nil
}

// Call направляет публичное имя на исходный сервер. tools/call после сбоя сети не повторяется.
func (h *Hub) Call(ctx context.Context, userID int64, publicName string, args json.RawMessage) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	h.mu.Lock()
	tool, ok := h.bind[userID][publicName]
	h.mu.Unlock()
	if !ok {
		return "Инструмент " + publicName + " не найден среди подключённых серверов.", true, nil
	}
	srv, err := h.Store.GetServer(ctx, userID, tool.ServerID)
	if err != nil {
		return "", false, err
	}
	if srv.ID == 0 || !srv.Enabled {
		return "Сервер " + tool.ServerName + " выключен или удалён.", true, nil
	}
	if srv.Revision != tool.Revision {
		return "Настройки сервера " + tool.ServerName + " изменились. Повторите запрос.", true, nil
	}
	if srv.Builder && agent.IsDangerous(tool.Original) {
		args = json.RawMessage(withConfirm(string(args)))
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	textOut, isErr, callErr := h.callOnce(callCtx, srv, tool.Original, args)
	if callErr == nil {
		return textOut, isErr, nil
	}
	if errors.Is(callErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "", false, callErr
	}
	if oauthUnauthorized(callErr) {
		if textOut, isErr, retryErr := h.callAfterRefresh(ctx, srv, tool.Original, args); retryErr == nil {
			return textOut, isErr, nil
		}
	}
	h.dropSession(srv)
	return "Сервер " + srv.Name + " не выполнил вызов: " + publicErr(callErr), true, nil
}

// Probe проверяет один сервер и возвращает текст без секретов.
func (h *Hub) Probe(ctx context.Context, userID, serverID int64) (string, error) {
	srv, err := h.Store.GetServer(ctx, userID, serverID)
	if err != nil {
		return "", err
	}
	if srv.ID == 0 {
		return "Сервер не найден.", nil
	}
	tools, err := h.listServer(ctx, srv)
	if err != nil {
		msg := publicErr(err)
		_ = h.Store.SetServerStatus(ctx, userID, serverID, false, msg)
		return srv.Name + " не подключился: " + msg, nil
	}
	_ = h.Store.SetServerStatus(ctx, userID, serverID, true, "")
	return fmt.Sprintf("%s подключён, инструментов: %d.", srv.Name, len(tools)), nil
}

// Forget закрывает сессии сервера после удаления или смены доступа.
func (h *Hub) Forget(userID, serverID int64) {
	h.mu.Lock()
	var drop []*openSession
	for key, sess := range h.sessions {
		if key.user == userID && key.server == serverID {
			drop = append(drop, sess)
			delete(h.sessions, key)
			delete(h.cached, key)
		}
	}
	delete(h.bind, userID)
	h.mu.Unlock()
	for _, sess := range drop {
		sess.Close()
	}
}

type openSession struct {
	sess *sdkmcp.ClientSession
	stop context.CancelFunc
}

func (s *openSession) Close() {
	if s == nil {
		return
	}
	if s.stop != nil {
		s.stop()
	}
	if s.sess != nil {
		_ = s.sess.Close()
	}
}

func (h *Hub) listServer(ctx context.Context, srv store.Server) ([]agent.Remote, error) {
	listCtx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	key := sessKey{user: srv.UserID, server: srv.ID, rev: srv.Revision}
	h.mu.Lock()
	if cached, ok := h.cached[key]; ok && h.CacheTTL > 0 && time.Since(cached.at) < h.CacheTTL {
		tools := cached.tools
		h.mu.Unlock()
		return bindTools(srv, tools), nil
	}
	h.mu.Unlock()
	sess, err := h.session(listCtx, srv)
	if err != nil {
		return nil, err
	}
	var all []*sdkmcp.Tool
	cursor := ""
	for page := 0; page < 20; page++ {
		res, err := sess.ListTools(listCtx, &sdkmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			h.dropSession(srv)
			return nil, err
		}
		if res != nil {
			all = append(all, res.Tools...)
			if res.NextCursor == "" || res.NextCursor == cursor {
				break
			}
			cursor = res.NextCursor
		}
	}
	h.mu.Lock()
	h.cached[key] = cachedTools{at: time.Now(), tools: all}
	h.mu.Unlock()
	return bindTools(srv, all), nil
}

func bindTools(srv store.Server, tools []*sdkmcp.Tool) []agent.Remote {
	out := make([]agent.Remote, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		if len(raw) == 0 || string(raw) == "null" {
			raw = []byte(`{"type":"object","properties":{}}`)
		}
		out = append(out, agent.Remote{
			Original:    tool.Name,
			ServerID:    srv.ID,
			ServerName:  srv.Name,
			Builder:     srv.Builder,
			Revision:    srv.Revision,
			Description: tool.Description,
			Schema:      raw,
		})
	}
	return out
}

func (h *Hub) callOnce(ctx context.Context, srv store.Server, name string, args json.RawMessage) (string, bool, error) {
	sess, err := h.session(ctx, srv)
	if err != nil {
		return "", false, err
	}
	var arguments any
	trimmed := strings.TrimSpace(string(args))
	if trimmed == "" {
		arguments = map[string]any{}
	} else if err := json.Unmarshal([]byte(trimmed), &arguments); err != nil {
		return "", false, fmt.Errorf("аргументы не JSON")
	}
	res, err := sess.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", false, err
	}
	return formatResult(res), res.IsError, nil
}

func (h *Hub) callAfterRefresh(ctx context.Context, srv store.Server, name string, args json.RawMessage) (string, bool, error) {
	creds, err := mcp.ParseCredentials(srv.AuthJSON)
	if err != nil || creds.RefreshToken == "" {
		return "", false, fmt.Errorf("нет refresh token")
	}
	creds.Expiry = time.Unix(0, 0)
	raw, err := creds.JSON()
	if err != nil {
		return "", false, err
	}
	srv.AuthJSON = raw
	if _, err := h.Store.PutServer(ctx, srv); err != nil {
		return "", false, err
	}
	fresh, err := h.Store.GetServer(ctx, srv.UserID, srv.ID)
	if err != nil {
		return "", false, err
	}
	h.dropSession(fresh)
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return h.callOnce(callCtx, fresh, name, args)
}

func (h *Hub) session(ctx context.Context, srv store.Server) (*sdkmcp.ClientSession, error) {
	key := sessKey{user: srv.UserID, server: srv.ID, rev: srv.Revision}
	h.mu.Lock()
	if sess := h.sessions[key]; sess != nil {
		h.mu.Unlock()
		return sess.sess, nil
	}
	var stale []*openSession
	for oldKey, sess := range h.sessions {
		if oldKey.user == srv.UserID && oldKey.server == srv.ID {
			stale = append(stale, sess)
			delete(h.sessions, oldKey)
			delete(h.cached, oldKey)
		}
	}
	h.mu.Unlock()
	for _, sess := range stale {
		sess.Close()
	}
	opened, err := h.open(ctx, srv)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if prev := h.sessions[key]; prev != nil {
		h.mu.Unlock()
		opened.Close()
		return prev.sess, nil
	}
	h.sessions[key] = opened
	h.mu.Unlock()
	return opened.sess, nil
}

func (h *Hub) dropSession(srv store.Server) {
	key := sessKey{user: srv.UserID, server: srv.ID, rev: srv.Revision}
	h.mu.Lock()
	sess := h.sessions[key]
	delete(h.sessions, key)
	delete(h.cached, key)
	h.mu.Unlock()
	sess.Close()
}

func (h *Hub) open(ctx context.Context, srv store.Server) (*openSession, error) {
	sessCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	var ready atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
			if !ready.Load() {
				stop()
			}
		case <-sessCtx.Done():
		}
	}()
	sess, err := h.dial(sessCtx, srv)
	ready.Store(true)
	if err != nil || ctx.Err() != nil {
		stop()
		if sess != nil {
			_ = sess.Close()
		}
		if err == nil {
			err = ctx.Err()
		}
		return nil, err
	}
	return &openSession{sess: sess, stop: stop}, nil
}

func (h *Hub) dial(ctx context.Context, srv store.Server) (*sdkmcp.ClientSession, error) {
	endpoint, err := h.endpoint(srv)
	if err != nil {
		return nil, err
	}
	if !h.AllowPrivate && mcp.IsPrivateURL(endpoint) {
		return nil, fmt.Errorf("локальный адрес запрещён настройкой MCP_ALLOW_PRIVATE")
	}
	creds, err := mcp.ParseCredentials(srv.AuthJSON)
	if err != nil {
		return nil, err
	}
	var src oauth2.TokenSource
	if srv.AuthKind == mcp.AuthOAuth {
		src, err = h.tokenSource(srv, creds)
		if err != nil {
			return nil, err
		}
	}
	client, err := authClient(endpoint, srv.AuthKind, creds, src)
	if err != nil {
		return nil, err
	}
	switch srv.Transport {
	case "sse":
		return connectSSE(ctx, endpoint, client)
	case "streamable":
		return connectStreamable(ctx, endpoint, client)
	default:
		sess, streamErr := connectStreamable(ctx, endpoint, client)
		if streamErr == nil {
			return sess, nil
		}
		if !tryLegacySSE(streamErr) {
			return nil, streamErr
		}
		sess, sseErr := connectSSE(ctx, endpoint, client)
		if sseErr != nil {
			return nil, streamErr
		}
		return sess, nil
	}
}

func (h *Hub) endpoint(srv store.Server) (string, error) {
	if srv.Builder && strings.TrimSpace(srv.URL) == "" {
		if strings.TrimSpace(h.BuilderURL) == "" {
			return "", fmt.Errorf("не задан адрес конструктора")
		}
		return h.BuilderURL, nil
	}
	if srv.Builder {
		return mcp.NormalizeURL(srv.URL)
	}
	return mcp.PreserveURL(srv.URL)
}

func connectStreamable(ctx context.Context, endpoint string, client *http.Client) (*sdkmcp.ClientSession, error) {
	sdk := sdkmcp.NewClient(&sdkmcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	return sdk.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           client,
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
}

func connectSSE(ctx context.Context, endpoint string, client *http.Client) (*sdkmcp.ClientSession, error) {
	sdk := sdkmcp.NewClient(&sdkmcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	return sdk.Connect(ctx, &sdkmcp.SSEClientTransport{Endpoint: endpoint, HTTPClient: client}, nil)
}

func tryLegacySSE(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "401") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "403") {
		return false
	}
	return strings.Contains(msg, "400") || strings.Contains(msg, "bad request") ||
		strings.Contains(msg, "404") || strings.Contains(msg, "not found") ||
		strings.Contains(msg, "405") || strings.Contains(msg, "method not allowed") ||
		strings.Contains(msg, "406") || strings.Contains(msg, "not acceptable") ||
		strings.Contains(msg, "415") || strings.Contains(msg, "unsupported media") ||
		strings.Contains(msg, "session")
}

func oauthUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "401") || strings.Contains(msg, "unauthorized")
}

func publicErr(err error) string {
	if err == nil {
		return ""
	}
	msg := text.Redact(err.Error())
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 240 {
		msg = msg[:240]
	}
	return msg
}

func withConfirm(args string) string {
	raw := strings.TrimSpace(args)
	if raw == "" {
		raw = "{}"
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return raw
	}
	m["confirm"] = true
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return string(b)
}
