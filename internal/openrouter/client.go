package openrouter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// ToolCall — вызов функции моделью.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message — реплика для chat completions.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Images     []string   `json:"-"`
}

// Tool — функция, которую можно отдать модели.
type Tool struct {
	Name        string
	Description string
	Parameters  any
}

// Request — один запрос к модели.
type Request struct {
	Model       string
	Messages    []Message
	Tools       []Tool
	MaxTokens   int
	Temperature float32
}

// Client ходит в OpenRouter совместимым с OpenAI API.
type Client struct {
	api *openai.Client
}

// New создаёт клиент с таймаутом 90 секунд.
func New(apiKey string) (*Client, error) {
	return NewAt(apiKey, "https://openrouter.ai/api/v1", &http.Client{Timeout: 90 * time.Second})
}

// NewAt собирает клиент с явным адресом. Нужен тестам.
func NewAt(apiKey, baseURL string, hc *http.Client) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("пустой ключ OpenRouter")
	}
	if hc == nil {
		hc = &http.Client{Timeout: 90 * time.Second}
	}
	cfg := openai.DefaultConfig(apiKey)
	cfg.BaseURL = baseURL
	cfg.HTTPClient = hc
	return &Client{api: openai.NewClientWithConfig(cfg)}, nil
}

// Complete выполняет chat completion. Пустой список Tools отключает инструменты.
func (c *Client) Complete(ctx context.Context, req Request) (Message, error) {
	messages := make([]openai.ChatCompletionMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := openai.ChatCompletionMessage{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		}
		if len(m.Images) > 0 {
			parts := make([]openai.ChatMessagePart, 0, 1+len(m.Images))
			if strings.TrimSpace(m.Content) != "" {
				parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeText, Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, openai.ChatMessagePart{
					Type:     openai.ChatMessagePartTypeImageURL,
					ImageURL: &openai.ChatMessageImageURL{URL: img},
				})
			}
			msg.Content = ""
			msg.MultiContent = parts
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		messages = append(messages, msg)
	}
	apiReq := openai.ChatCompletionRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
	}
	if len(req.Tools) > 0 {
		apiReq.ToolChoice = "auto"
		for _, tool := range req.Tools {
			params := tool.Parameters
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			apiReq.Tools = append(apiReq.Tools, openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  params,
				},
			})
		}
	}
	resp, err := c.api.CreateChatCompletion(ctx, apiReq)
	if err != nil {
		return Message{}, err
	}
	if len(resp.Choices) == 0 {
		return Message{}, fmt.Errorf("пустой ответ модели")
	}
	choice := resp.Choices[0].Message
	out := Message{Role: choice.Role, Content: choice.Content}
	if out.Role == "" {
		out.Role = openai.ChatMessageRoleAssistant
	}
	for _, tc := range choice.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: args,
		})
	}
	return out, nil
}
