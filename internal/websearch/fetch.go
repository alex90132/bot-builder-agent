package websearch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// Page — текст страницы без разметки.
type Page struct {
	URL   string
	Title string
	Text  string
}

const defaultFetchChars = 12000

// FetchURL скачивает страницу и достаёт читаемый текст.
func (c *Client) FetchURL(ctx context.Context, pageURL string, maxChars int) (*Page, error) {
	pageURL = strings.TrimSpace(pageURL)
	if pageURL == "" {
		return nil, fmt.Errorf("пустой адрес")
	}
	if !strings.HasPrefix(pageURL, "http://") && !strings.HasPrefix(pageURL, "https://") {
		pageURL = "https://" + pageURL
	}
	if maxChars <= 0 {
		maxChars = defaultFetchChars
	}
	body, ct, err := c.download(ctx, pageURL)
	if err != nil {
		return nil, err
	}
	htmlStr := decodeBody(body, ct)
	if !looksLikeMarkup(htmlStr) && !strings.Contains(ct, "text/") {
		return nil, fmt.Errorf("fetch %s: неподдерживаемый тип %s", pageURL, ct)
	}
	title, text := extractText(htmlStr)
	text = collapseWhitespace(text)
	if utf8.RuneCountInString(strings.TrimSpace(text)) < 60 {
		if meta := extractMetaDescription(htmlStr); meta != "" {
			if strings.TrimSpace(text) == "" {
				text = meta
			} else {
				text = meta + "\n" + text
			}
		}
	}
	if utf8.RuneCountInString(text) > maxChars {
		text = string([]rune(text)[:maxChars]) + "…"
	}
	return &Page{URL: pageURL, Title: strings.TrimSpace(title), Text: strings.TrimSpace(text)}, nil
}

func (c *Client) download(ctx context.Context, pageURL string) ([]byte, string, error) {
	body, contentType, err := downloadWith(ctx, c.fetchc, pageURL)
	if err == nil || c.directc == nil {
		return body, contentType, err
	}
	body, contentType, directErr := downloadWith(ctx, c.directc, pageURL)
	if directErr != nil {
		return nil, "", err
	}
	return body, contentType, nil
}

func downloadWith(ctx context.Context, client *http.Client, pageURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:120.0) Gecko/20100101 Firefox/120.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru,en;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch %s: %w", pageURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("fetch %s: HTTP %d", pageURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", pageURL, err)
	}
	return body, strings.ToLower(resp.Header.Get("Content-Type")), nil
}

func decodeBody(body []byte, contentType string) string {
	reader, err := charset.NewReader(strings.NewReader(string(body)), contentType)
	if err != nil {
		return string(body)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return string(body)
	}
	return string(decoded)
}

func looksLikeMarkup(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s))
	if len(head) > 2048 {
		head = head[:2048]
	}
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html") ||
		strings.Contains(head, "<body") || strings.Contains(head, "<div") || strings.Contains(head, "<p")
}

func extractText(htmlStr string) (string, string) {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return "", ""
	}
	title := extractTitle(doc)
	root := findNode(doc, "article", "main")
	if root == nil {
		root = findNode(doc, "body")
	}
	if root == nil {
		root = doc
	}
	skip := map[string]bool{
		"script": true, "style": true, "noscript": true, "nav": true,
		"footer": true, "header": true, "aside": true, "form": true,
		"svg": true, "iframe": true, "button": true, "select": true,
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && skip[n.Data] {
			return
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				sb.WriteString(t)
				sb.WriteString(" ")
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "br", "li", "h1", "h2", "h3", "h4", "h5", "tr", "section", "article", "pre", "blockquote":
				sb.WriteString("\n")
			}
		}
	}
	walk(root)
	body := sb.String()
	if utf8.RuneCountInString(strings.TrimSpace(body)) < 200 && root != doc {
		if full := findNode(doc, "body"); full != nil && full != root {
			sb.Reset()
			walk(full)
			body = sb.String()
		}
	}
	return title, body
}

func extractTitle(doc *html.Node) string {
	if n := findNode(doc, "title"); n != nil && n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
		if t := strings.TrimSpace(n.FirstChild.Data); t != "" {
			return t
		}
	}
	if n := findNode(doc, "h1"); n != nil {
		var sb strings.Builder
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.TextNode {
				sb.WriteString(ch.Data)
			}
		}
		return strings.TrimSpace(sb.String())
	}
	return ""
}

func findNode(root *html.Node, tags ...string) *html.Node {
	want := map[string]bool{}
	for _, t := range tags {
		want[t] = true
	}
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && want[n.Data] {
			found = n
			return
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(root)
	return found
}

func extractMetaDescription(htmlStr string) string {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return ""
	}
	var desc, ogDesc, ogTitle string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "meta" {
			var name, prop, content string
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "name":
					name = strings.ToLower(a.Val)
				case "property":
					prop = strings.ToLower(a.Val)
				case "content":
					content = a.Val
				}
			}
			if content != "" {
				if name == "description" && desc == "" {
					desc = content
				}
				if prop == "og:description" && ogDesc == "" {
					ogDesc = content
				}
				if prop == "og:title" && ogTitle == "" {
					ogTitle = content
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	parts := make([]string, 0, 2)
	if ogTitle != "" {
		parts = append(parts, ogTitle)
	}
	if desc != "" {
		parts = append(parts, desc)
	} else if ogDesc != "" {
		parts = append(parts, ogDesc)
	}
	return strings.TrimSpace(strings.Join(parts, ": "))
}

func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		ln = strings.Join(strings.Fields(ln), " ")
		if ln != "" {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}
