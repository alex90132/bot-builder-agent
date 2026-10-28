package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bot-builder-agent/internal/openrouter"
)

func localTools() []openrouter.Tool {
	return []openrouter.Tool{
		{
			Name:        "web_search",
			Description: "Ищет информацию в интернете. Используй для свежих фактов, новостей, цен, проверки утверждений. Возвращает короткие выжимки: для деталей открой ссылку через fetch_url.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Запрос словами человека, 3–8 слов, без подмены темы.",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "fetch_url",
			Description: "Открывает страницу по ссылке и читает её текст. Вызывай, когда в чате скинули ссылку, и после web_search, чтобы ответить по странице, а не по обрывку выдачи.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "Полный URL как в сообщении или в результатах поиска.",
					},
				},
				"required": []string{"url"},
			},
		},
	}
}

func (r *Runner) localTool(ctx context.Context, call openrouter.ToolCall) (string, bool) {
	switch call.Name {
	case "web_search":
		return r.webSearch(ctx, call.Arguments), true
	case "fetch_url":
		return r.fetchURL(ctx, call.Arguments), true
	default:
		return "", false
	}
}

func (r *Runner) webSearch(ctx context.Context, raw string) string {
	if r.Web == nil {
		return "Поиск не настроен."
	}
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil || strings.TrimSpace(args.Query) == "" {
		return "Ошибка: пустой поисковый запрос."
	}
	results, err := r.Web.Search(ctx, args.Query, 6)
	if err != nil {
		return fmt.Sprintf("Поиск не удался: %v", err)
	}
	if len(results) == 0 {
		return "Ничего не найдено."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Результаты поиска по «%s»:\n", args.Query))
	for i, item := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n%s\n%s\n\n", i+1, item.Title, item.URL, item.Content))
	}
	sb.WriteString("Это короткие выжимки. Если нужны детали или точные цифры, вызови fetch_url на подходящей ссылке и отвечай по её тексту.")
	return sb.String()
}

func (r *Runner) fetchURL(ctx context.Context, raw string) string {
	if r.Web == nil {
		return "Открытие страниц не настроено."
	}
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil || strings.TrimSpace(args.URL) == "" {
		return "Ошибка: пустой URL."
	}
	page, err := r.Web.FetchURL(ctx, args.URL, 12000)
	if err != nil {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Страница %s недоступна. Не вызывай fetch_url для неё повторно.\n", args.URL))
		if results, e := r.Web.Search(ctx, args.URL, 3); e == nil && len(results) > 0 {
			sb.WriteString("Информация из поиска по этой ссылке:\n")
			for i, item := range results {
				sb.WriteString(fmt.Sprintf("%d. %s\n%s\n%s\n\n", i+1, item.Title, item.URL, item.Content))
			}
		}
		return sb.String()
	}
	if strings.TrimSpace(page.Text) == "" {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Страница %s не отдала читаемый текст. Заголовок: %s.\n", page.URL, page.Title)
		query := strings.TrimSpace(page.Title)
		if query == "" {
			query = args.URL
		}
		if results, e := r.Web.Search(ctx, query, 3); e == nil && len(results) > 0 {
			sb.WriteString("Информация из поиска:\n")
			for i, item := range results {
				fmt.Fprintf(&sb, "%d. %s\n%s\n%s\n\n", i+1, item.Title, item.URL, item.Content)
			}
		}
		return sb.String()
	}
	return fmt.Sprintf("Текст страницы %s (заголовок: %s):\n\n%s", page.URL, page.Title, page.Text)
}
