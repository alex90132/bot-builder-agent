package mcphub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	localmcp "bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/store"
)

func TestSeveralServersSameToolName(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]string{}
	newSrv := func(label, secret string) *httptest.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: label, Version: "1"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in struct {
			Text string `json:"text"`
		}) (*mcp.CallToolResult, map[string]any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: label + ":" + in.Text}},
			}, map[string]any{"from": label}, nil
		})
		inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[label] = append(seen[label], r.Header.Get("Authorization"))
			mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer "+secret {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			if r.URL.RawQuery != "keep=1" {
				http.Error(w, "query", http.StatusBadRequest)
				return
			}
			inner.ServeHTTP(w, r)
		}))
	}
	alphaSrv := newSrv("alpha", "alpha-secret")
	defer alphaSrv.Close()
	betaSrv := newSrv("beta", "beta-secret")
	defer betaSrv.Close()

	st := openStore(t)
	ctx := context.Background()
	if _, err := st.PutServer(ctx, store.Server{
		UserID: 1, Name: "alpha", URL: alphaSrv.URL + "/mcp?keep=1", Transport: "streamable",
		AuthKind: "bearer", AuthJSON: mustJSON(t, map[string]string{"token": "alpha-secret"}), Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutServer(ctx, store.Server{
		UserID: 1, Name: "beta", URL: betaSrv.URL + "/mcp?keep=1", Transport: "streamable",
		AuthKind: "bearer", AuthJSON: mustJSON(t, map[string]string{"token": "beta-secret"}), Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutServer(ctx, store.Server{
		UserID: 2, Name: "alpha", URL: alphaSrv.URL + "/mcp?keep=1", Transport: "streamable",
		AuthKind: "bearer", AuthJSON: mustJSON(t, map[string]string{"token": "other-secret"}), Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	hub := New(st, "http://127.0.0.1", time.Hour)
	set, err := hub.Catalog(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Tools) != 2 || len(set.Failures) != 0 {
		t.Fatalf("tools %+v failures %v", set.Tools, set.Failures)
	}
	byServer := map[string]string{}
	for _, tool := range set.Tools {
		if tool.Original != "echo" || tool.PublicName == "echo" || tool.PublicName == "web_search" || tool.PublicName == "fetch_url" {
			t.Fatalf("имя %+v", tool)
		}
		byServer[tool.ServerName] = tool.PublicName
	}
	if byServer["alpha"] == "" || byServer["alpha"] == byServer["beta"] {
		t.Fatalf("имена %+v", byServer)
	}
	got, isErr, err := hub.Call(ctx, 1, byServer["alpha"], []byte(`{"text":"x"}`))
	if err != nil || isErr || !strings.Contains(got, "alpha:x") || !strings.Contains(got, `"from":"alpha"`) {
		t.Fatalf("alpha %q isErr=%v %v", got, isErr, err)
	}
	got, isErr, err = hub.Call(ctx, 1, byServer["beta"], []byte(`{"text":"y"}`))
	if err != nil || isErr || !strings.Contains(got, "beta:y") || strings.Contains(got, "alpha:y") {
		t.Fatalf("beta %q isErr=%v %v", got, isErr, err)
	}
	if _, err := hub.Catalog(ctx, 2); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !hasHeader(seen["alpha"], "Bearer alpha-secret") || !hasHeader(seen["alpha"], "Bearer other-secret") {
		t.Fatalf("изоляция чатов: %+v", seen)
	}
	for _, header := range seen["beta"] {
		if header != "" && header != "Bearer beta-secret" {
			t.Fatalf("чужой токен у beta: %q", header)
		}
	}
}

func TestDownServerDoesNotBlockTheOther(t *testing.T) {
	up := toolServer(t, "up")
	defer up.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	st := openStore(t)
	ctx := context.Background()
	if _, err := st.PutServer(ctx, store.Server{
		UserID: 1, Name: "down", URL: down.URL, Transport: "streamable", AuthKind: "none", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutServer(ctx, store.Server{
		UserID: 1, Name: "up", URL: up.URL, Transport: "streamable", AuthKind: "none", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	hub := New(st, "http://127.0.0.1", time.Hour)
	set, err := hub.Catalog(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Tools) != 1 || set.Tools[0].ServerName != "up" || len(set.Failures) != 1 || !strings.Contains(set.Failures[0], "down") {
		t.Fatalf("%+v %v", set.Tools, set.Failures)
	}
	text, isErr, err := hub.Call(ctx, 1, set.Tools[0].PublicName, []byte(`{}`))
	if err != nil || isErr || !strings.Contains(text, "up") {
		t.Fatalf("%q %v %v", text, isErr, err)
	}
}

func TestCacheIsolatedByChatAndSettings(t *testing.T) {
	var mu sync.Mutex
	var lists int
	var auth []string
	server := mcp.NewServer(&mcp.Implementation{Name: "cache", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "p"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		if strings.Contains(string(body), "tools/list") {
			lists++
			auth = append(auth, r.Header.Get("Authorization"))
		}
		mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()

	st := openStore(t)
	ctx := context.Background()
	first, err := st.PutServer(ctx, store.Server{
		UserID: 1, Name: "cache", URL: srv.URL, Transport: "streamable", AuthKind: "bearer",
		AuthJSON: mustJSON(t, map[string]string{"token": "one"}), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutServer(ctx, store.Server{
		UserID: 2, Name: "cache", URL: srv.URL, Transport: "streamable", AuthKind: "bearer",
		AuthJSON: mustJSON(t, map[string]string{"token": "two"}), Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	hub := New(st, "http://127.0.0.1", time.Hour)
	if _, err := hub.Catalog(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Catalog(ctx, 1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if lists != 1 {
		t.Fatalf("кэш не сработал: %d", lists)
	}
	mu.Unlock()
	if _, err := hub.Catalog(ctx, 2); err != nil {
		t.Fatal(err)
	}
	first.AuthJSON = mustJSON(t, map[string]string{"token": "three"})
	if _, err := st.PutServer(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Catalog(ctx, 1); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if lists != 3 {
		t.Fatalf("списков %d, ждали 3", lists)
	}
	if !hasHeader(auth, "Bearer one") || !hasHeader(auth, "Bearer two") || !hasHeader(auth, "Bearer three") {
		t.Fatalf("заголовки %+v", auth)
	}
}

func TestAuthModesAndRedirectKeepsSecrets(t *testing.T) {
	var got http.Header
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer secret.Close()
	var foreign http.Header
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreign = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()
	jump := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/land", http.StatusFound)
	}))
	defer jump.Close()

	creds := mustCreds(t, `{"token":"secret-token","headers":{"X-Extra":"hidden"}}`)
	client, err := authClient(jump.URL, "bearer", creds, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(jump.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if foreign.Get("Authorization") != "" || foreign.Get("X-Extra") != "" {
		t.Fatalf("секрет ушёл на другой origin: auth %q extra %q", foreign.Get("Authorization"), foreign.Get("X-Extra"))
	}

	modes := []struct {
		kind string
		json string
		want func(http.Header) bool
	}{
		{"none", `{}`, func(h http.Header) bool { return h.Get("Authorization") == "" && h.Get("X-API-Key") == "" }},
		{"bearer", `{"token":"abc"}`, func(h http.Header) bool { return h.Get("Authorization") == "Bearer abc" }},
		{"api_key", `{"header":"X-API-Key","token":"k"}`, func(h http.Header) bool { return h.Get("X-API-Key") == "k" }},
		{"basic", `{"username":"u","password":"p"}`, func(h http.Header) bool {
			return h.Get("Authorization") == "Basic "+base64.StdEncoding.EncodeToString([]byte("u:p"))
		}},
		{"headers", `{"headers":{"X-Custom":"v"}}`, func(h http.Header) bool { return h.Get("X-Custom") == "v" }},
	}
	for _, mode := range modes {
		got = nil
		parsed := mustCreds(t, mode.json)
		client, err := authClient(secret.URL, mode.kind, parsed, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Get(secret.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if !mode.want(got) {
			t.Fatalf("%s headers %+v", mode.kind, got)
		}
	}
}

func TestStatefulSessionLegacySSEAndProtocol(t *testing.T) {
	var sawSession, sawProto bool
	stateMCP := mcp.NewServer(&mcp.Implementation{Name: "state", Version: "1"}, nil)
	mcp.AddTool(stateMCP, &mcp.Tool{Name: "ping", Description: "p"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "state-pong"}}}, nil, nil
	})
	stateInner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return stateMCP }, nil)
	stateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Mcp-Session-Id") != "" && strings.Contains(string(body), "tools/call") {
			sawSession = true
		}
		if r.Header.Get("MCP-Protocol-Version") != "" && strings.Contains(string(body), "tools/call") {
			sawProto = true
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		stateInner.ServeHTTP(w, r)
	}))

	sseMCP := mcp.NewServer(&mcp.Implementation{Name: "sse", Version: "1"}, nil)
	mcp.AddTool(sseMCP, &mcp.Tool{Name: "ping", Description: "p"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "sse-pong"}}}, nil, nil
	})
	sseSrv := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return sseMCP }, nil))
	var hub *Hub
	st := openStore(t)
	t.Cleanup(func() {
		release(hub, st, 1)
		sseSrv.Close()
		stateSrv.Close()
	})
	ctx := context.Background()
	for _, item := range []store.Server{
		{UserID: 1, Name: "state", URL: stateSrv.URL, Transport: "streamable", AuthKind: "none", Enabled: true},
		{UserID: 1, Name: "legacy", URL: sseSrv.URL, Transport: "sse", AuthKind: "none", Enabled: true},
		{UserID: 1, Name: "auto", URL: sseSrv.URL, Transport: "auto", AuthKind: "none", Enabled: true},
	} {
		if _, err := st.PutServer(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	hub = New(st, "http://127.0.0.1", time.Hour)
	set, err := hub.Catalog(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, tool := range set.Tools {
		byName[tool.ServerName] = tool.PublicName
	}
	for _, name := range []string{"state", "legacy", "auto"} {
		text, isErr, err := hub.Call(ctx, 1, byName[name], []byte(`{}`))
		if err != nil || isErr || !strings.Contains(text, "pong") {
			t.Fatalf("%s %q err=%v isErr=%v failures=%v", name, text, err, isErr, set.Failures)
		}
	}
	if !sawSession {
		t.Fatal("stateful вызов ушёл без MCP-Session-Id")
	}
	if !sawProto {
		t.Fatal("после initialize нет MCP-Protocol-Version")
	}
}

func TestOAuthDynamicAndPreregisteredRefresh(t *testing.T) {
	t.Run("dynamic", func(t *testing.T) {
		runOAuth(t, "", true)
	})
	t.Run("preregistered", func(t *testing.T) {
		runOAuth(t, "preset", false)
	})
}

func TestOAuthRejectsDPoPAndMutualTLS(t *testing.T) {
	cases := []struct {
		name, field, want string
	}{
		{"dpop", "dpop_bound_access_tokens_required", "DPoP"},
		{"mtls", "tls_client_certificate_bound_access_tokens", "mutual TLS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var srv *httptest.Server
			mux := http.NewServeMux()
			mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				http.Error(w, "no", http.StatusUnauthorized)
			})
			mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				body := map[string]any{
					"resource":              srv.URL + "/mcp",
					"authorization_servers": []string{srv.URL},
				}
				body[tc.field] = true
				_ = json.NewEncoder(w).Encode(body)
			})
			srv = httptest.NewServer(mux)
			defer srv.Close()
			st := openStore(t)
			item, err := st.PutServer(context.Background(), store.Server{
				UserID: 1, Name: tc.name, URL: srv.URL + "/mcp", Transport: "streamable", AuthKind: "oauth", Enabled: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			hub := New(st, "http://127.0.0.1", time.Hour)
			hub.PublicURL = "http://127.0.0.1:9"
			_, _, err = hub.BeginOAuth(context.Background(), 1, item.ID)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestUnsupportedAuthAndPrivateFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Custom realm="x"`)
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	st := openStore(t)
	ctx := context.Background()
	item, err := st.PutServer(ctx, store.Server{UserID: 1, Name: "custom", URL: srv.URL + "/mcp", Transport: "streamable", AuthKind: "oauth", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	hub := New(st, "http://127.0.0.1", time.Hour)
	hub.PublicURL = "http://127.0.0.1:9"
	if _, _, err := hub.BeginOAuth(ctx, 1, item.ID); err == nil || !strings.Contains(err.Error(), "не поддерживает") {
		t.Fatal(err)
	}
	hub.AllowPrivate = false
	msg, err := hub.Probe(ctx, 1, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "MCP_ALLOW_PRIVATE") {
		t.Fatalf("probe %q", msg)
	}
}

func TestCallIsNotRetriedAfterHTTPFailure(t *testing.T) {
	var calls int
	server := mcp.NewServer(&mcp.Implementation{Name: "once", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "p"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil, nil
	})
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "tools/call") {
			calls++
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		inner.ServeHTTP(w, r)
	}))
	defer srv.Close()
	st := openStore(t)
	ctx := context.Background()
	if _, err := st.PutServer(ctx, store.Server{UserID: 1, Name: "once", URL: srv.URL, Transport: "streamable", AuthKind: "none", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	hub := New(st, "http://127.0.0.1", time.Hour)
	set, err := hub.Catalog(ctx, 1)
	if err != nil || len(set.Tools) != 1 {
		t.Fatal(err, set)
	}
	text, isErr, err := hub.Call(ctx, 1, set.Tools[0].PublicName, []byte(`{}`))
	if err != nil || !isErr || strings.Contains(text, "pong") {
		t.Fatalf("text %q isErr %v err %v", text, isErr, err)
	}
	if calls != 1 {
		t.Fatalf("повтор tools/call: %d", calls)
	}
}

func TestFormatKeepsNonText(t *testing.T) {
	got := formatResult(&mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.ImageContent{MIMEType: "image/png", Data: []byte("abcd")},
			&mcp.AudioContent{MIMEType: "audio/mpeg", Data: []byte("xyz")},
			&mcp.ResourceLink{Name: "file", URI: "file://a", MIMEType: "text/plain"},
			&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "mem://n", Text: "hello"}},
			&mcp.ToolUseContent{Name: "nested", Input: map[string]any{"a": 1}},
		},
		StructuredContent: map[string]any{"ok": true},
	})
	for _, part := range []string{"изображение", "аудио", "file://a", "hello", "nested", `"ok":true`} {
		if !strings.Contains(got, part) {
			t.Fatalf("нет %q в %s", part, got)
		}
	}
}

func runOAuth(t *testing.T, clientID string, wantRegister bool) {
	t.Helper()
	var mu sync.Mutex
	access := "none"
	var challenge string
	var registerHits int
	var refreshed bool
	var srv *httptest.Server
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "oa", Version: "1"}, nil)
	mcp.AddTool(mcpServer, &mcp.Tool{Name: "ping", Description: "p"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil, nil
	})
	stream := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+access
		mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		stream.ServeHTTP(w, r)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resource":                 srv.URL + "/mcp",
			"authorization_servers":    []string{srv.URL},
			"bearer_methods_supported": []string{"header"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                         srv.URL,
			"authorization_endpoint":                         srv.URL + "/authorize",
			"token_endpoint":                                 srv.URL + "/token",
			"registration_endpoint":                          srv.URL + "/register",
			"response_types_supported":                       []string{"code"},
			"code_challenge_methods_supported":               []string{"S256"},
			"scopes_supported":                               []string{"mcp", "offline_access"},
			"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
			"token_endpoint_auth_methods_supported":          []string{"client_secret_post"},
			"authorization_response_iss_parameter_supported": true,
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		registerHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"client_id":                  "dyn",
			"client_secret":              "sec",
			"token_endpoint_auth_method": "client_secret_post",
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("resource") == "" || q.Get("state") == "" {
			http.Error(w, "bad auth", http.StatusBadRequest)
			return
		}
		if clientID != "" && q.Get("client_id") != clientID {
			http.Error(w, "client", http.StatusBadRequest)
			return
		}
		mu.Lock()
		challenge = q.Get("code_challenge")
		mu.Unlock()
		redir := q.Get("redirect_uri") + "?code=good&state=" + url.QueryEscape(q.Get("state")) + "&iss=" + url.QueryEscape(srv.URL)
		http.Redirect(w, r, redir, http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("resource") == "" {
			http.Error(w, "no resource", http.StatusBadRequest)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			mu.Lock()
			match := base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
			mu.Unlock()
			if !match {
				http.Error(w, "pkce", http.StatusBadRequest)
				return
			}
			mu.Lock()
			access = "access-1"
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600, "token_type": "Bearer"})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "refresh-1" {
				http.Error(w, "refresh", http.StatusBadRequest)
				return
			}
			mu.Lock()
			access = "access-2"
			refreshed = true
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "refresh_token": "refresh-1", "expires_in": 3600, "token_type": "Bearer"})
		default:
			http.Error(w, "grant", http.StatusBadRequest)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st := openStore(t)
	ctx := context.Background()
	raw := "{}"
	if clientID != "" {
		raw = mustJSON(t, map[string]string{"client_id": clientID, "client_secret": "sec"})
	}
	item, err := st.PutServer(ctx, store.Server{
		UserID: 1, Name: "oauth", URL: srv.URL + "/mcp", Transport: "streamable", AuthKind: "oauth", AuthJSON: raw, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub := New(st, "http://127.0.0.1", time.Hour)
	cb := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeHTTP(w, r)
	}))
	cb.Start()
	t.Cleanup(func() {
		release(hub, st, 1)
		cb.Close()
	})
	hub.PublicURL = cb.URL
	link, done, err := hub.BeginOAuth(ctx, 1, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Авторизация завершена") {
		t.Fatalf("callback %d %s", resp.StatusCode, body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("oauth не завершился")
	}
	mu.Lock()
	if wantRegister && registerHits == 0 {
		t.Fatal("не было dynamic registration")
	}
	if !wantRegister && registerHits != 0 {
		t.Fatal("заранее зарегистрированный клиент пошёл в registration")
	}
	mu.Unlock()
	set, err := hub.Catalog(ctx, 1)
	if err != nil || len(set.Tools) != 1 || len(set.Failures) != 0 {
		t.Fatalf("catalog %+v %v", set, err)
	}
	text, isErr, err := hub.Call(ctx, 1, set.Tools[0].PublicName, []byte(`{}`))
	if err != nil || isErr || !strings.Contains(text, "ok") {
		t.Fatalf("call %q %v %v", text, isErr, err)
	}
	mu.Lock()
	access = "gone"
	mu.Unlock()
	text, isErr, err = hub.Call(ctx, 1, set.Tools[0].PublicName, []byte(`{}`))
	mu.Lock()
	did401 := refreshed
	mu.Unlock()
	if err != nil || isErr || !strings.Contains(text, "ok") || !did401 {
		t.Fatalf("повтор после 401: %q isErr %v err %v refreshed %v", text, isErr, err, did401)
	}
	saved, err := st.GetServer(ctx, 1, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	creds := mustCreds(t, saved.AuthJSON)
	if creds.RefreshToken != "refresh-1" || creds.AccessToken == "" || strings.Contains(saved.Name, creds.AccessToken) {
		t.Fatalf("creds saved name %s", saved.Name)
	}
	if clientID == "" && creds.ClientID != "dyn" {
		t.Fatalf("client id %q", creds.ClientID)
	}
	beforeRev := saved.Revision
	creds.Expiry = time.Unix(0, 0)
	encoded, err := creds.JSON()
	if err != nil {
		t.Fatal(err)
	}
	saved.AuthJSON = encoded
	saved, err = st.PutServer(ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != beforeRev {
		t.Fatal("обновление срока OAuth сменило revision и сбросило бы сессию")
	}
	hub.Forget(1, item.ID)
	set, err = hub.Catalog(ctx, 1)
	if err != nil || len(set.Failures) != 0 {
		t.Fatalf("после refresh %+v %v", set, err)
	}
	text, isErr, err = hub.Call(ctx, 1, set.Tools[0].PublicName, []byte(`{}`))
	mu.Lock()
	did := refreshed
	mu.Unlock()
	if err != nil || isErr || !strings.Contains(text, "ok") || !did {
		t.Fatalf("refresh %q isErr %v err %v refreshed %v failures %v", text, isErr, err, did, set.Failures)
	}
}

func release(hub *Hub, st *store.Store, userID int64) {
	if hub == nil || st == nil {
		return
	}
	items, err := st.ListServers(context.Background(), userID)
	if err != nil {
		return
	}
	for _, item := range items {
		hub.Forget(userID, item.ID)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("sqlite://"+t.TempDir()+"/a.db", 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func toolServer(t *testing.T, label string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: label, Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "e"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: label}}}, nil, nil
	})
	return httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
}

func hasHeader(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustCreds(t *testing.T, raw string) localmcp.Credentials {
	t.Helper()
	creds, err := localmcp.ParseCredentials(raw)
	if err != nil {
		t.Fatal(err)
	}
	return creds
}
