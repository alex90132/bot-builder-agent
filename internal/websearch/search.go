package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/proxy"
)

// Result — одна строка выдачи.
type Result struct {
	Title   string
	URL     string
	Content string
	Engine  string
}

// Client ходит в SearXNG и скачивает страницы.
type Client struct {
	searxURL string
	httpc    *http.Client
	fetchc   *http.Client
	directc  *http.Client
}

const (
	searchEngines   = "google,duckduckgo,yandex,wikipedia"
	fallbackEngines = "bing,mojeek"
	fetchTimeout    = 35 * time.Second
)

// New создаёт клиент. Пустой searxURL значит http://localhost:8890.
// proxyURL пустой: fetch_url идёт напрямую. Иначе socks5:// или http://.
func New(searxURL, proxyURL string) *Client {
	if strings.TrimSpace(searxURL) == "" {
		searxURL = "http://localhost:8890"
	}
	c := &Client{
		searxURL: strings.TrimRight(strings.TrimSpace(searxURL), "/"),
		httpc:    &http.Client{Timeout: 25 * time.Second},
		fetchc:   buildFetchClient(proxyURL),
	}
	if strings.TrimSpace(proxyURL) != "" {
		c.directc = buildFetchClient("")
	}
	return c
}

func buildFetchClient(proxyURL string) *http.Client {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return &http.Client{Timeout: fetchTimeout}
	}
	tr := &http.Transport{
		MaxIdleConns:        20,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return &http.Client{Timeout: fetchTimeout}
	}
	if strings.HasPrefix(strings.ToLower(u.Scheme), "socks5") {
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		if d, derr := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct); derr == nil {
			if cd, ok := d.(proxy.ContextDialer); ok {
				tr.DialContext = cd.DialContext
			} else {
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return d.Dial(network, addr)
				}
			}
		}
	} else {
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Timeout: fetchTimeout, Transport: tr}
}

// Search спрашивает SearXNG и возвращает не больше limit результатов.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("пустой запрос")
	}
	if limit <= 0 {
		limit = 5
	}
	out, err := c.searchWith(ctx, query, limit, searchEngines)
	if err == nil && len(out) > 0 {
		return out, nil
	}
	if fallback, ferr := c.searchWith(ctx, query, limit, fallbackEngines); ferr == nil && len(fallback) > 0 {
		return fallback, nil
	}
	return out, err
}

func (c *Client) searchWith(ctx context.Context, query string, limit int, engines string) ([]Result, error) {
	u, err := url.Parse(c.searxURL + "/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("language", queryLanguage(query))
	q.Set("safesearch", "0")
	q.Set("engines", engines)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "bot-builder-agent/1.0")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng status %d", resp.StatusCode)
	}
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
			Engine  string `json:"engine"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("searxng decode: %w", err)
	}
	out := make([]Result, 0, limit)
	for _, r := range payload.Results {
		title := strings.TrimSpace(r.Title)
		link := strings.TrimSpace(r.URL)
		if title == "" || link == "" || isEngineArtifact(link) {
			continue
		}
		out = append(out, Result{
			Title:   title,
			URL:     link,
			Content: strings.TrimSpace(r.Content),
			Engine:  strings.TrimSpace(r.Engine),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func isEngineArtifact(link string) bool {
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	path := strings.ToLower(u.Path)
	switch {
	case host == "bing.com", strings.HasSuffix(host, ".bing.com"):
		return true
	case host == "yandex.ru" && strings.HasPrefix(path, "/currency_converter"):
		return true
	case host == "duckduckgo.com" && (path == "" || path == "/"):
		return true
	case (host == "google.com" || strings.HasPrefix(host, "google.")) && strings.HasPrefix(path, "/search"):
		return true
	}
	return false
}

func queryLanguage(query string) string {
	for _, r := range query {
		if unicode.Is(unicode.Cyrillic, r) {
			return "ru"
		}
	}
	return "en"
}
