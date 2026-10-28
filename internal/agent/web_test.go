package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/openrouter"
	"bot-builder-agent/internal/websearch"
)

func TestRunnerSearchesInsteadOfInventing(t *testing.T) {
	var mcpCalls int
	mcpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), `"method":"initialize"`):
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`)
		case strings.Contains(string(body), "notifications/initialized"):
			w.WriteHeader(http.StatusAccepted)
		case strings.Contains(string(body), "tools/list"):
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo","description":"e","inputSchema":{"type":"object"}}]}}`)
		default:
			mcpCalls++
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"mcp"}]}}`)
		}
	}))
	defer mcpSrv.Close()

	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"title":"Доллар","url":"https://news.example/usd","content":"96 рублей","engine":"google"}]}`)
	}))
	defer searx.Close()

	var sawImage bool
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "image_url") && strings.Contains(string(body), "data:image/png;base64,qq") {
			sawImage = true
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"role":"tool"`) {
			writeChoice(w, "курс 96", "")
			return
		}
		writeChoice(w, "", `{"id":"c1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"курс доллара\"}"}}`)
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
		OR:          orClient,
		MCP:         mcpClient,
		Web:         websearch.New(searx.URL, ""),
		Model:       "test",
		MaxRounds:   4,
		MaxTokens:   200,
		Temperature: 0,
	}
	res, err := runner.Run(context.Background(), Request{
		Token:    "mcp_exampletoken",
		UserText: "какой курс",
		Images:   []string{"data:image/png;base64,qq"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reply != "курс 96" {
		t.Fatalf("reply %q", res.Reply)
	}
	if !sawImage {
		t.Fatal("картинка не ушла в модель")
	}
	if mcpCalls != 0 {
		t.Fatal("поиск не должен звать MCP")
	}
	for _, msg := range res.Transcript {
		if strings.Contains(msg.Content, "base64") {
			t.Fatal("картинка попала в историю")
		}
	}
	found := false
	for _, msg := range res.Transcript {
		if msg.Role == "tool" && strings.Contains(msg.Content, "https://news.example/usd") {
			found = true
		}
	}
	if !found {
		t.Fatal("в истории нет выдачи")
	}
}
