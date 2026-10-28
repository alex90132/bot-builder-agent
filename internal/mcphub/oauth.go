package mcphub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/store"
)

const oauthWaitLimit = 10 * time.Minute

type oauthPending struct {
	ch       chan *auth.AuthorizationResult
	userID   int64
	serverID int64
}

type capturedToken struct {
	cfg *oauth2.Config
	tok *oauth2.Token
}

// BeginOAuth запускает Authorization Code с PKCE и возвращает ссылку для пользователя.
// Канал done закрывает фоновый обмен кода на токен.
func (h *Hub) BeginOAuth(ctx context.Context, userID, serverID int64) (string, <-chan error, error) {
	if strings.TrimSpace(h.PublicURL) == "" {
		return "", nil, fmt.Errorf("задайте OAUTH_PUBLIC_URL. Redirect URI: этот адрес и путь /oauth/callback")
	}
	srv, err := h.Store.GetServer(ctx, userID, serverID)
	if err != nil {
		return "", nil, err
	}
	if srv.ID == 0 {
		return "", nil, fmt.Errorf("сервер не найден")
	}
	if srv.AuthKind != mcp.AuthOAuth {
		return "", nil, fmt.Errorf("у сервера не выбран OAuth")
	}
	endpoint, err := h.endpoint(srv)
	if err != nil {
		return "", nil, err
	}
	if !h.AllowPrivate && mcp.IsPrivateURL(endpoint) {
		return "", nil, fmt.Errorf("локальный адрес запрещён настройкой MCP_ALLOW_PRIVATE")
	}
	creds, err := mcp.ParseCredentials(srv.AuthJSON)
	if err != nil {
		return "", nil, err
	}
	metaClient := &http.Client{Timeout: 20 * time.Second}
	if err := unsupportedAuth(ctx, endpoint, metaClient); err != nil {
		return "", nil, err
	}
	redirect := strings.TrimRight(strings.TrimSpace(h.PublicURL), "/") + "/oauth/callback"
	cfg := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:           redirect,
		RequestRefreshToken:   true,
		AcceptUnadvertisedIss: true,
		Client:                metaClient,
		ScopeFilter: func(discovered []string) []string {
			if len(creds.Scopes) > 0 {
				return append([]string(nil), creds.Scopes...)
			}
			return discovered
		},
	}
	if strings.TrimSpace(creds.ClientID) != "" {
		pre := &oauthex.ClientCredentials{ClientID: creds.ClientID}
		if strings.TrimSpace(creds.ClientSecret) != "" {
			pre.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: creds.ClientSecret}
		}
		cfg.PreregisteredClient = pre
	} else {
		cfg.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				RedirectURIs:            []string{redirect},
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				TokenEndpointAuthMethod: "client_secret_post",
				ClientName:              "bot-builder-agent",
			},
		}
	}
	urlCh := make(chan string, 1)
	early := make(chan error, 1)
	cfg.AuthorizationCodeFetcher = func(waitCtx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		parsed, err := url.Parse(args.URL)
		if err != nil {
			return nil, err
		}
		state := parsed.Query().Get("state")
		if state == "" {
			return nil, fmt.Errorf("сервер авторизации не вернул state")
		}
		wait := &oauthPending{ch: make(chan *auth.AuthorizationResult, 1), userID: userID, serverID: serverID}
		h.mu.Lock()
		h.pending[state] = wait
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			delete(h.pending, state)
			h.mu.Unlock()
		}()
		urlCh <- args.URL
		select {
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		case <-time.After(oauthWaitLimit):
			return nil, fmt.Errorf("время авторизации вышло")
		case res := <-wait.ch:
			if res == nil {
				return nil, fmt.Errorf("авторизация отменена")
			}
			return res, nil
		}
	}
	got := &capturedToken{}
	cfg.NewTokenSource = func(srcCtx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
		got.cfg = oc
		got.tok = tok
		return oc.TokenSource(srcCtx, tok), nil
	}
	handler, err := auth.NewAuthorizationCodeHandler(cfg)
	if err != nil {
		return "", nil, err
	}
	// NewTokenSource must be set before Authorize. The handler already copied config by pointer,
	// so the field is visible.
	flowCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), oauthWaitLimit)
	done := make(chan error, 1)
	go func() {
		defer cancel()
		err := handler.Authorize(flowCtx, oauthProbeRequest(endpoint), oauthProbeResponse())
		if err != nil {
			select {
			case early <- err:
			default:
			}
			done <- err
			return
		}
		done <- h.saveOAuth(flowCtx, srv, creds, endpoint, got)
	}()
	select {
	case <-ctx.Done():
		cancel()
		return "", nil, ctx.Err()
	case link := <-urlCh:
		return link, done, nil
	case err := <-early:
		return "", nil, err
	case <-time.After(30 * time.Second):
		cancel()
		return "", nil, fmt.Errorf("сервер авторизации не ответил")
	}
}

func (h *Hub) saveOAuth(ctx context.Context, srv store.Server, creds mcp.Credentials, endpoint string, got *capturedToken) error {
	if got == nil || got.tok == nil || got.cfg == nil || got.tok.AccessToken == "" {
		return fmt.Errorf("сервер авторизации не выдал токен")
	}
	creds.ClientID = got.cfg.ClientID
	if got.cfg.ClientSecret != "" {
		creds.ClientSecret = got.cfg.ClientSecret
	}
	creds.AuthURL = got.cfg.Endpoint.AuthURL
	creds.TokenURL = got.cfg.Endpoint.TokenURL
	creds.Scopes = append([]string(nil), got.cfg.Scopes...)
	creds.Resource = endpoint
	creds.AccessToken = got.tok.AccessToken
	if got.tok.RefreshToken != "" {
		creds.RefreshToken = got.tok.RefreshToken
	}
	creds.Expiry = got.tok.Expiry
	raw, err := creds.JSON()
	if err != nil {
		return err
	}
	srv.AuthJSON = raw
	srv.AuthKind = mcp.AuthOAuth
	if _, err := h.Store.PutServer(ctx, srv); err != nil {
		return err
	}
	h.Forget(srv.UserID, srv.ID)
	return nil
}

// ServeHTTP принимает OAuth callback. State сверяется с выданным ожиданием.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/oauth/callback" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if errText := strings.TrimSpace(q.Get("error")); errText != "" {
		h.rejectState(q.Get("state"), errText)
		http.Error(w, "Авторизация отклонена.", http.StatusBadRequest)
		return
	}
	if !h.finishState(q.Get("state"), q.Get("code"), q.Get("iss")) {
		http.Error(w, "Неизвестный или просроченный state.", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, "<p>Авторизация завершена. Вернитесь в Telegram.</p>")
}

func (h *Hub) finishState(state, code, iss string) bool {
	state = strings.TrimSpace(state)
	code = strings.TrimSpace(code)
	if state == "" || code == "" {
		return false
	}
	h.mu.Lock()
	wait := h.pending[state]
	h.mu.Unlock()
	if wait == nil {
		return false
	}
	wait.ch <- &auth.AuthorizationResult{Code: code, State: state, Iss: iss}
	return true
}

func (h *Hub) rejectState(state, reason string) {
	h.mu.Lock()
	wait := h.pending[strings.TrimSpace(state)]
	h.mu.Unlock()
	if wait == nil {
		return
	}
	wait.ch <- nil
	_ = reason
}

func unsupportedAuth(ctx context.Context, endpoint string, client *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
		if err != nil {
			return fmt.Errorf("сервер прислал непонятный WWW-Authenticate")
		}
		for _, challenge := range challenges {
			if challenge.Scheme != "" && challenge.Scheme != "bearer" {
				return fmt.Errorf("сервер требует авторизацию %s, этот бот её не поддерживает", challenge.Scheme)
			}
		}
	}
	return checkResourceMetadata(ctx, endpoint, client)
}

func checkResourceMetadata(ctx context.Context, endpoint string, client *http.Client) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	origin := u.Scheme + "://" + u.Host
	candidates := []string{
		origin + "/.well-known/oauth-protected-resource/" + strings.TrimLeft(u.Path, "/"),
		origin + "/.well-known/oauth-protected-resource",
	}
	for _, metaURL := range candidates {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, metaURL, endpoint, client)
		if err != nil || prm == nil {
			continue
		}
		if prm.DPOPBoundAccessTokensRequired {
			return fmt.Errorf("сервер требует DPoP, этот бот его не поддерживает")
		}
		if prm.TLSClientCertificateBoundAccessTokens {
			return fmt.Errorf("сервер требует mutual TLS, этот бот его не поддерживает")
		}
		if len(prm.BearerMethodsSupported) > 0 && !slices.Contains(prm.BearerMethodsSupported, "header") {
			return fmt.Errorf("сервер не принимает Bearer в заголовке Authorization")
		}
		return nil
	}
	return nil
}

func oauthProbeRequest(endpoint string) *http.Request {
	req, _ := http.NewRequest(http.MethodPost, endpoint, nil)
	return req
}

func oauthProbeResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

type refreshing struct {
	mu     sync.Mutex
	hub    *Hub
	server store.Server
	creds  mcp.Credentials
}

func (h *Hub) tokenSource(srv store.Server, creds mcp.Credentials) (oauth2.TokenSource, error) {
	if strings.TrimSpace(creds.AccessToken) == "" && strings.TrimSpace(creds.RefreshToken) == "" {
		return nil, fmt.Errorf("сначала пройдите OAuth по ссылке из карточки сервера")
	}
	return &refreshing{hub: h, server: srv, creds: creds}, nil
}

func (r *refreshing) Token() (*oauth2.Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tok := &oauth2.Token{
		AccessToken:  r.creds.AccessToken,
		RefreshToken: r.creds.RefreshToken,
		Expiry:       r.creds.Expiry,
		TokenType:    "Bearer",
	}
	if tok.Valid() {
		return tok, nil
	}
	if r.creds.RefreshToken == "" || r.creds.TokenURL == "" {
		return nil, fmt.Errorf("нужна повторная авторизация OAuth")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", r.creds.RefreshToken)
	if r.creds.ClientID != "" {
		form.Set("client_id", r.creds.ClientID)
	}
	if r.creds.ClientSecret != "" {
		form.Set("client_secret", r.creds.ClientSecret)
	}
	if r.creds.Resource != "" {
		form.Set("resource", r.creds.Resource)
	}
	req, err := http.NewRequest(http.MethodPost, r.creds.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("не обновить токен")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("повторная авторизация не удалась, http %d", resp.StatusCode)
	}
	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.AccessToken == "" {
		return nil, fmt.Errorf("сервер авторизации вернул пустой токен")
	}
	r.creds.AccessToken = parsed.AccessToken
	if parsed.RefreshToken != "" {
		r.creds.RefreshToken = parsed.RefreshToken
	}
	if parsed.ExpiresIn > 0 {
		r.creds.Expiry = time.Now().Add(time.Duration(parsed.ExpiresIn) * time.Second)
	} else {
		r.creds.Expiry = time.Time{}
	}
	if r.hub != nil && r.hub.Store != nil && r.server.ID != 0 {
		raw, err := r.creds.JSON()
		if err == nil {
			r.server.AuthJSON = raw
			_, _ = r.hub.Store.PutServer(context.Background(), r.server)
		}
	}
	return &oauth2.Token{AccessToken: r.creds.AccessToken, RefreshToken: r.creds.RefreshToken, Expiry: r.creds.Expiry, TokenType: "Bearer"}, nil
}
