package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"bot-builder-agent/internal/mcp"
	"bot-builder-agent/internal/openrouter"
	"bot-builder-agent/internal/text"
	"bot-builder-agent/internal/websearch"
)

const forceText = "Инструменты больше недоступны. Ответь пользователю обычным текстом по уже собранной информации. Не пиши названия функций."

// Runner гоняет цикл инструментов между OpenRouter и MCP.
type Runner struct {
	OR             *openrouter.Client
	MCP            *mcp.Client
	Web            *websearch.Client
	Remotes        Remotes
	Model          string
	MaxRounds      int
	MaxResultChars int
	MaxTokens      int
	Temperature    float32
}

// Request — один ход пользователя.
type Request struct {
	Token         string
	History       []openrouter.Message
	UserText      string
	Images        []string
	UserID        int64
	Endpoint      string
	ActiveProject int64
	OnTool        func(name string)
}

// Result — ответ пользователю и реплики, которые стоит записать в историю.
type Result struct {
	Reply      string
	Transcript []openrouter.Message
}

type toolOut struct {
	id      string
	name    string
	content string
	fatal   error
}

// Run выполняет ход. Токен уходит только в MCP.
func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	// Локальные копии: один Runner обслуживает все чаты сразу.
	maxRounds := r.MaxRounds
	if maxRounds < 1 {
		maxRounds = 16
	}
	maxChars := r.MaxResultChars
	if maxChars <= 0 {
		maxChars = 12000
	}
	names, otools, remotes, prompt, err := r.loadTools(ctx, req)
	if err != nil {
		return Result{}, err
	}

	msgs := []openrouter.Message{{
		Role:    "system",
		Content: prompt,
	}}
	msgs = append(msgs, req.History...)
	userMsg := openrouter.Message{Role: "user", Content: req.UserText, Images: req.Images}
	msgs = append(msgs, userMsg)
	stored := userMsg
	stored.Images = nil
	transcript := []openrouter.Message{stored}

	for round := 0; round < maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		resp, err := r.OR.Complete(ctx, openrouter.Request{
			Model:       r.Model,
			Messages:    msgs,
			Tools:       otools,
			MaxTokens:   r.MaxTokens,
			Temperature: r.Temperature,
		})
		if err != nil {
			return Result{}, err
		}
		if len(resp.ToolCalls) == 0 {
			if name, args, ok := DetectTextualCall(resp.Content, names); ok {
				resp.ToolCalls = []openrouter.ToolCall{{
					ID:        fmt.Sprintf("textual-%d", round),
					Name:      name,
					Arguments: args,
				}}
				resp.Content = ""
			} else {
				resp.Content = cleanFinal(resp.Content, names)
				resp.Role = "assistant"
				transcript = append(transcript, resp)
				return Result{Reply: resp.Content, Transcript: transcript}, nil
			}
		}
		outs, err := r.execTools(ctx, req, resp.ToolCalls, maxChars, remotes)
		if err != nil {
			return Result{}, err
		}
		resp.Role = "assistant"
		msgs = append(msgs, resp)
		transcript = append(transcript, resp)
		for _, out := range outs {
			msg := openrouter.Message{
				Role:       "tool",
				Name:       out.name,
				ToolCallID: out.id,
				Content:    out.content,
			}
			msgs = append(msgs, msg)
			transcript = append(transcript, msg)
		}
	}

	msgs = append(msgs, openrouter.Message{Role: "user", Content: forceText})
	resp, err := r.OR.Complete(ctx, openrouter.Request{
		Model:       r.Model,
		Messages:    msgs,
		MaxTokens:   r.MaxTokens,
		Temperature: r.Temperature,
	})
	if err != nil {
		return Result{}, err
	}
	resp.Role = "assistant"
	resp.ToolCalls = nil
	resp.Content = cleanFinal(resp.Content, names)
	transcript = append(transcript, resp)
	return Result{Reply: resp.Content, Transcript: transcript}, nil
}

func cleanFinal(content string, names []string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "Модель вернула пустой ответ."
	}
	if LooksLikeRawToolCall(content, names) {
		return "Не удалось выполнить действие. Сформулируйте запрос ещё раз."
	}
	return content
}

func (r *Runner) loadTools(ctx context.Context, req Request) ([]string, []openrouter.Tool, map[string]Remote, string, error) {
	remotes := map[string]Remote{}
	var names []string
	var otools []openrouter.Tool
	hasBuilder := req.UserID == 0
	var failures []string
	if r.Remotes != nil && req.UserID != 0 {
		set, err := r.Remotes.Catalog(ctx, req.UserID)
		if err != nil {
			return nil, nil, nil, "", err
		}
		hasBuilder = set.HasBuilder
		failures = set.Failures
		for _, tool := range set.Tools {
			remotes[tool.PublicName] = tool
			names = append(names, tool.PublicName)
			params := any(tool.Schema)
			if len(tool.Schema) == 0 || string(tool.Schema) == "null" {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			otools = append(otools, openrouter.Tool{
				Name:        tool.PublicName,
				Description: tool.Description,
				Parameters:  params,
			})
		}
	} else if r.MCP != nil {
		tools, err := r.MCP.ListTools(ctx, req.Endpoint, req.Token)
		if err != nil {
			return nil, nil, nil, "", err
		}
		for _, tool := range tools {
			names = append(names, tool.Name)
			params := any(tool.InputSchema)
			if len(tool.InputSchema) == 0 || string(tool.InputSchema) == "null" {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			otools = append(otools, openrouter.Tool{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  params,
			})
		}
	}
	for _, tool := range localTools() {
		if _, taken := remotes[tool.Name]; taken {
			continue
		}
		names = append(names, tool.Name)
		otools = append(otools, tool)
	}
	return names, otools, remotes, SystemPromptFor(req.ActiveProject, hasBuilder, failures), nil
}

func (r *Runner) execTools(ctx context.Context, req Request, calls []openrouter.ToolCall, maxChars int, remotes map[string]Remote) ([]toolOut, error) {
	outs := make([]toolOut, len(calls))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	var fatal error
	var fatalMu sync.Mutex
	for i, call := range calls {
		if IsDangerous(call.Name) {
			call.Arguments = withConfirm(call.Arguments)
		}
		wg.Add(1)
		go func(i int, call openrouter.ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if req.OnTool != nil {
				req.OnTool(call.Name)
			}
			if textOut, ok := r.localTool(ctx, call); ok {
				outs[i] = toolOut{id: call.ID, name: call.Name, content: text.Truncate(textOut, maxChars)}
				return
			}
			if tool, ok := remotes[call.Name]; ok && r.Remotes != nil {
				if tool.Builder && IsDangerous(tool.Original) {
					call.Arguments = withConfirm(call.Arguments)
				}
				textOut, isErr, err := r.Remotes.Call(ctx, req.UserID, call.Name, json.RawMessage(call.Arguments))
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						fatalMu.Lock()
						if fatal == nil {
							fatal = err
						}
						fatalMu.Unlock()
						return
					}
					textOut = "Ошибка вызова: " + err.Error()
				}
				if isErr && strings.TrimSpace(textOut) == "" {
					textOut = "Инструмент вернул ошибку."
				}
				outs[i] = toolOut{id: call.ID, name: call.Name, content: text.Truncate(textOut, maxChars)}
				return
			}
			textOut, isErr, err := r.MCP.CallTool(ctx, req.Endpoint, req.Token, call.Name, json.RawMessage(call.Arguments))
			if err != nil {
				if errors.Is(err, mcp.ErrUnauthorized) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					fatalMu.Lock()
					if fatal == nil {
						fatal = err
					}
					fatalMu.Unlock()
					return
				}
				textOut = "Ошибка вызова: " + err.Error()
			}
			if isErr && strings.TrimSpace(textOut) == "" {
				textOut = "Инструмент вернул ошибку."
			}
			outs[i] = toolOut{id: call.ID, name: call.Name, content: text.Truncate(textOut, maxChars)}
		}(i, call)
	}
	wg.Wait()
	if fatal != nil {
		return nil, fatal
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return outs, nil
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
