package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/openrouter"
)

func TestPromptHidesToken(t *testing.T) {
	p := SystemPrompt(12)
	if !strings.Contains(p, "латиниц") || !strings.Contains(p, "12") {
		t.Fatal(p)
	}
	if strings.Contains(p, "mcp_") {
		t.Fatal("в промпте не должно быть токена")
	}
	if strings.Contains(p, "спросит подтверждение") || strings.Contains(p, "Да / Нет") {
		t.Fatal("промпт всё ещё обещает спросить подтверждение")
	}
	if !strings.Contains(p, "только если пользователь явно попросил") {
		t.Fatal("промпт должен запрещать выдуманные опасные действия")
	}
}

func TestRemoteFailureStillAnswersInText(t *testing.T) {
	var body string
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		writeChoice(w, "сервер docs не отвечает, остальные вызовы доступны", "")
	}))
	defer orSrv.Close()
	orClient, err := openrouter.NewAt("test-key", orSrv.URL+"/v1", orSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		OR: orClient, Remotes: downRemotes{}, Model: "test-model",
		MaxRounds: 2, MaxResultChars: 1000, MaxTokens: 50,
	}
	res, err := runner.Run(context.Background(), Request{UserID: 7, UserText: "что с серверами"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "docs: connection refused") {
		t.Fatal(body)
	}
	if strings.Contains(body, "латиниц") {
		t.Fatal("промпт конструктора ушёл без конструктора")
	}
	if !strings.Contains(body, "web_search") || !strings.Contains(body, "notes__ping") {
		t.Fatal(body)
	}
	if !strings.Contains(res.Reply, "не отвечает") {
		t.Fatal(res.Reply)
	}
}

type downRemotes struct{}

func (downRemotes) Catalog(context.Context, int64) (RemoteSet, error) {
	return RemoteSet{
		Failures: []string{"docs: connection refused"},
		Tools: []Remote{{
			PublicName:  "notes__ping",
			Original:    "ping",
			ServerName:  "notes",
			Description: "ping",
			Schema:      []byte(`{"type":"object","properties":{}}`),
		}},
	}, nil
}

func (downRemotes) Call(context.Context, int64, string, json.RawMessage) (string, bool, error) {
	return "", false, errors.New("вызов не ожидался")
}

func TestTextualCall(t *testing.T) {
	name, args, ok := DetectTextualCall("db_list_projects({\"archived\":false})", []string{"db_list", "db_list_projects"})
	if !ok || name != "db_list_projects" || !strings.Contains(args, "archived") {
		t.Fatalf("%s %s %v", name, args, ok)
	}
	if _, _, ok := DetectTextualCall("просто текст про db_list_projects", []string{"db_list_projects"}); ok {
		t.Fatal("обычный текст не вызов")
	}
}

func TestRunnerToolsConfirmTruncateAndSecret(t *testing.T) {
	var mu sync.Mutex
	var orBodies []string
	var mcpCalls []string
	var mcpArgs []map[string]any
	var mcpAuth []string

	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		mcpAuth = append(mcpAuth, r.Header.Get("Authorization"))
		if strings.Contains(string(body), "tools/call") {
			var req struct {
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.Unmarshal(body, &req)
			mcpCalls = append(mcpCalls, req.Params.Name)
			mcpArgs = append(mcpArgs, req.Params.Arguments)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "initialize"):
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`)
		case strings.Contains(string(body), "notifications/initialized"):
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(string(body), "tools/list"):
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[
				{"name":"db_list_projects","description":"list","inputSchema":{"type":"object"}},
				{"name":"db_stop_bot","description":"stop","inputSchema":{"type":"object"}},
				{"name":"echo","description":"echo","inputSchema":{"type":"object"}}
			]}}`)
		default:
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"`+strings.Repeat("x", 80)+`"}],"isError":false}}`)
		}
	}))
	defer mcpSrv.Close()

	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		orBodies = append(orBodies, string(body))
		n := len(orBodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var content string
		switch n {
		case 1:
			content = `{"id":"c1","type":"function","function":{"name":"db_stop_bot","arguments":"{}"}}`
			writeChoice(w, "", content)
		case 2:
			writeChoice(w, "готово", "")
		default:
			writeChoice(w, "ещё", "")
		}
	}))
	defer orSrv.Close()

	mcpClient, err := mcp.New(mcpSrv.URL, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	orClient, err := openrouter.NewAt("test-key", orSrv.URL+"/v1", orSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{
		OR: orClient, MCP: mcpClient, Model: "test-model",
		MaxRounds: 4, MaxResultChars: 30, MaxTokens: 100, Temperature: 0,
	}
	secret := "mcp_should_not_leak_into_openrouter"
	res, err := runner.Run(context.Background(), Request{
		Token:    secret,
		UserText: "останови",
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	stopped := false
	for i, call := range mcpCalls {
		if call != "db_stop_bot" {
			continue
		}
		stopped = true
		if mcpArgs[i]["confirm"] != true {
			t.Fatalf("db_stop_bot без confirm:true: %+v", mcpArgs[i])
		}
	}
	if !stopped {
		t.Fatal("опасный тул не ушёл в MCP")
	}
	if !strings.Contains(res.Reply, "готово") {
		t.Fatalf("reply %q", res.Reply)
	}
	for _, body := range orBodies {
		if strings.Contains(body, secret) {
			t.Fatal("токен попал в OpenRouter")
		}
	}
	for _, msg := range res.Transcript {
		if msg.Role == "tool" && strings.Contains(msg.Content, "не подтвердил") {
			t.Fatal("отказ записан вместо вызова")
		}
	}
	for _, auth := range mcpAuth {
		if auth != "" && auth != "Bearer "+secret {
			t.Fatalf("auth %q", auth)
		}
	}
}

func TestRunnerParallelTruncateDangerTextLimitCancel(t *testing.T) {
	t.Run("parallel", func(t *testing.T) {
		var mu sync.Mutex
		calls := map[string]int{}
		var orBodies []string
		mcpSrv := mcpServer(t, func(name string, args map[string]any) {
			mu.Lock()
			calls[name]++
			mu.Unlock()
		})
		defer mcpSrv.Close()
		orSrv := scriptServer(t, &orBodies, func(n int, body string) (content, tools string) {
			if n == 1 {
				return "", `{"id":"a","type":"function","function":{"name":"echo","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"db_list_projects","arguments":"{}"}}`
			}
			return "ок", ""
		})
		defer orSrv.Close()
		res, err := testRunner(t, mcpSrv, orSrv, 4, 40).Run(context.Background(), Request{Token: "mcp_x", UserText: "два"})
		if err != nil {
			t.Fatal(err)
		}
		if calls["echo"] == 0 || calls["db_list_projects"] == 0 {
			t.Fatalf("вызовы: %+v", calls)
		}
		for _, msg := range res.Transcript {
			if msg.Role == "tool" && (strings.Count(msg.Content, "y") >= 100 || !strings.Contains(msg.Content, "обрезано")) {
				t.Fatalf("ответ тула не обрезан: %s", msg.Content)
			}
		}
	})

	t.Run("danger", func(t *testing.T) {
		sawConfirm := false
		mcpSrv := mcpServer(t, func(name string, args map[string]any) {
			if name == "db_stop_bot" && args["confirm"] == true {
				sawConfirm = true
			}
		})
		defer mcpSrv.Close()
		var bodies []string
		orSrv := scriptServer(t, &bodies, func(n int, body string) (string, string) {
			if n == 1 {
				return "", `{"id":"s","type":"function","function":{"name":"db_stop_bot","arguments":"{}"}}`
			}
			return "остановлен", ""
		})
		defer orSrv.Close()
		res, err := testRunner(t, mcpSrv, orSrv, 4, 200).Run(context.Background(), Request{
			Token: "mcp_x", UserText: "стоп",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !sawConfirm || res.Reply != "остановлен" {
			t.Fatalf("confirm=%v reply=%q", sawConfirm, res.Reply)
		}
	})

	t.Run("textual", func(t *testing.T) {
		mcpSrv := mcpServer(t, nil)
		defer mcpSrv.Close()
		var bodies []string
		orSrv := scriptServer(t, &bodies, func(n int, body string) (string, string) {
			if n == 1 {
				return "db_list_projects({})", ""
			}
			return "список готов", ""
		})
		defer orSrv.Close()
		res, err := testRunner(t, mcpSrv, orSrv, 4, 200).Run(context.Background(), Request{Token: "mcp_x", UserText: "список"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Reply, "db_list_projects") {
			t.Fatalf("сырой вызов утёк: %s", res.Reply)
		}
	})

	t.Run("limit", func(t *testing.T) {
		mcpSrv := mcpServer(t, nil)
		defer mcpSrv.Close()
		var bodies []string
		orSrv := scriptServer(t, &bodies, func(n int, body string) (string, string) {
			if strings.Contains(body, `"tools":`) {
				return "", `{"id":"z","type":"function","function":{"name":"echo","arguments":"{}"}}`
			}
			return "финал", ""
		})
		defer orSrv.Close()
		res, err := testRunner(t, mcpSrv, orSrv, 1, 200).Run(context.Background(), Request{Token: "mcp_x", UserText: "круг"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Reply != "финал" {
			t.Fatalf("финал %q", res.Reply)
		}
		if len(bodies) < 2 || strings.Contains(bodies[len(bodies)-1], `"tools":`) {
			t.Fatal("финальный запрос всё ещё предлагает tools")
		}
	})

	t.Run("cancel", func(t *testing.T) {
		mcpSrv := mcpServer(t, nil)
		defer mcpSrv.Close()
		orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			timer := time.NewTimer(300 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
			case <-timer.C:
				w.WriteHeader(http.StatusGatewayTimeout)
			}
		}))
		defer orSrv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(30 * time.Millisecond)
			cancel()
		}()
		_, err := testRunner(t, mcpSrv, orSrv, 4, 200).Run(ctx, Request{Token: "mcp_x", UserText: "жди"})
		if err == nil {
			t.Fatal("ждали отмену")
		}
	})
}

func testRunner(t *testing.T, mcpSrv, orSrv *httptest.Server, rounds, maxChars int) *Runner {
	t.Helper()
	mc, err := mcp.New(mcpSrv.URL, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	oc, err := openrouter.NewAt("k", orSrv.URL+"/v1", orSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{OR: oc, MCP: mc, Model: "m", MaxRounds: rounds, MaxResultChars: maxChars, MaxTokens: 50, Temperature: 0}
}

func mcpServer(t *testing.T, onCall func(name string, args map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "initialize"):
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`)
		case strings.Contains(string(body), "notifications/initialized"):
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(string(body), "tools/list"):
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[
				{"name":"echo","description":"e","inputSchema":{"type":"object"}},
				{"name":"db_stop_bot","description":"s","inputSchema":{"type":"object"}},
				{"name":"db_list_projects","description":"l","inputSchema":{"type":"object"}}
			]}}`)
		default:
			var req struct {
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			_ = json.Unmarshal(body, &req)
			if onCall != nil {
				onCall(req.Params.Name, req.Params.Arguments)
			}
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"`+strings.Repeat("y", 100)+`"}]}}`)
		}
	}))
}

func scriptServer(t *testing.T, bodies *[]string, next func(n int, body string) (content, tools string)) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(body))
		n := len(*bodies)
		mu.Unlock()
		content, tools := next(n, string(body))
		w.Header().Set("Content-Type", "application/json")
		writeChoice(w, content, tools)
	}))
}

func writeChoice(w http.ResponseWriter, content, toolCalls string) {
	tools := ""
	if toolCalls != "" {
		tools = `,"tool_calls":[` + toolCalls + `]`
	}
	_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+jsonString(content)+tools+`}}]}`)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
