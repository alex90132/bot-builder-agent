package mcphub

import (
	"encoding/json"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func formatResult(res *sdkmcp.CallToolResult) string {
	if res == nil {
		return "Инструмент вернул пустой результат."
	}
	var parts []string
	for _, block := range res.Content {
		parts = append(parts, formatContent(block))
	}
	if res.StructuredContent != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			parts = append(parts, "[structuredContent не разобрать]")
		} else {
			parts = append(parts, string(raw))
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" && res.IsError {
		return "Инструмент вернул ошибку."
	}
	if text == "" {
		return "Инструмент вернул пустой результат."
	}
	return text
}

func formatContent(block sdkmcp.Content) string {
	switch item := block.(type) {
	case *sdkmcp.TextContent:
		return item.Text
	case *sdkmcp.ImageContent:
		return fmt.Sprintf("[изображение %s, %d байт]", item.MIMEType, len(item.Data))
	case *sdkmcp.AudioContent:
		return fmt.Sprintf("[аудио %s, %d байт]", item.MIMEType, len(item.Data))
	case *sdkmcp.ResourceLink:
		return fmt.Sprintf("[ресурс %s %s %s]", item.Name, item.URI, item.MIMEType)
	case *sdkmcp.EmbeddedResource:
		return formatResource(item.Resource)
	case *sdkmcp.ToolResultContent:
		raw, _ := json.Marshal(item.StructuredContent)
		return "[результат вложенного вызова] " + string(raw)
	case *sdkmcp.ToolUseContent:
		raw, _ := json.Marshal(item.Input)
		return "[запрос вложенного вызова " + item.Name + "] " + string(raw)
	default:
		raw, err := json.Marshal(block)
		if err != nil {
			return fmt.Sprintf("[контент %T]", block)
		}
		return fmt.Sprintf("[контент %T] %s", block, raw)
	}
}

func formatResource(res *sdkmcp.ResourceContents) string {
	if res == nil {
		return "[встроенный ресурс без данных]"
	}
	if strings.TrimSpace(res.Text) != "" {
		return fmt.Sprintf("[ресурс %s]\n%s", res.URI, res.Text)
	}
	if len(res.Blob) > 0 {
		return fmt.Sprintf("[ресурс %s %s, %d байт]", res.URI, res.MIMEType, len(res.Blob))
	}
	return fmt.Sprintf("[ресурс %s без текста]", res.URI)
}
