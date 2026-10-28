package telegram

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/mymmrac/telego"
)

const (
	maxImages     = 4
	maxImageBytes = 6 << 20
)

func imageFileIDs(msg *telego.Message) []string {
	if msg == nil {
		return nil
	}
	ids := photoFileIDs(msg)
	if msg.ReplyToMessage != nil {
		ids = append(ids, photoFileIDs(msg.ReplyToMessage)...)
	}
	if len(ids) > maxImages {
		ids = ids[:maxImages]
	}
	return ids
}

func photoFileIDs(msg *telego.Message) []string {
	if msg == nil {
		return nil
	}
	if len(msg.Photo) > 0 {
		return []string{msg.Photo[len(msg.Photo)-1].FileID}
	}
	if msg.Document != nil && strings.HasPrefix(strings.ToLower(msg.Document.MimeType), "image/") {
		return []string{msg.Document.FileID}
	}
	return nil
}

func (b *Bot) loadImages(ctx context.Context, fileIDs []string) []string {
	if len(fileIDs) == 0 {
		return nil
	}
	var out []string
	used := 0
	for _, id := range fileIDs {
		if len(out) >= maxImages || used >= maxImageBytes {
			break
		}
		body, mime, err := b.api.Download(ctx, id)
		if err != nil || len(body) == 0 {
			b.log.Warn("не удалось скачать изображение user file=%s: %v", id, err)
			continue
		}
		if used+len(body) > maxImageBytes {
			break
		}
		used += len(body)
		if mime == "" {
			mime = "image/jpeg"
		}
		out = append(out, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(body))
	}
	return out
}

func (a *liveAPI) Download(ctx context.Context, fileID string) ([]byte, string, error) {
	file, err := a.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, "", err
	}
	if file == nil || strings.TrimSpace(file.FilePath) == "" {
		return nil, "", fmt.Errorf("пустой файл")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.FileDownloadURL(file.FilePath), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("telegram file http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxImageBytes {
		return nil, "", fmt.Errorf("изображение больше %d байт", maxImageBytes)
	}
	return body, mimeByPath(file.FilePath), nil
}

func mimeByPath(filePath string) string {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}
