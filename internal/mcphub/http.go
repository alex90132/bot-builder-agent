package mcphub

import (
	"net/http"
	"net/url"
	"strings"

	"bot-builder-agent/internal/mcp"
	"golang.org/x/oauth2"
)

// originTransport добавляет секреты только запросам на исходный хост.
// Редирект на другой origin уходит без Authorization и без секретных заголовков.
type originTransport struct {
	base   http.RoundTripper
	origin *url.URL
	kind   string
	creds  mcp.Credentials
	src    oauth2.TokenSource
}

func (t *originTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	next := req.Clone(req.Context())
	if next.Header == nil {
		next.Header = make(http.Header)
	}
	if mcp.SameOrigin(t.origin, next.URL) {
		if t.src != nil {
			tok, err := t.src.Token()
			if err != nil {
				return nil, err
			}
			if tok != nil && tok.AccessToken != "" {
				next.Header.Set("Authorization", "Bearer "+tok.AccessToken)
			}
		} else {
			t.creds.Apply(t.kind, next.Header)
		}
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(next)
}

func stripSecrets(header http.Header, kind string, creds mcp.Credentials) {
	header.Del("Authorization")
	header.Del("Proxy-Authorization")
	if kind == mcp.AuthAPIKey {
		name := strings.TrimSpace(creds.Header)
		if name == "" {
			name = "X-API-Key"
		}
		header.Del(name)
	}
	for key := range creds.Headers {
		header.Del(key)
	}
}

func authClient(endpoint, kind string, creds mcp.Credentials, src oauth2.TokenSource) (*http.Client, error) {
	origin, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: &originTransport{
			base:   http.DefaultTransport,
			origin: origin,
			kind:   kind,
			creds:  creds,
			src:    src,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return http.ErrUseLastResponse
			}
			if len(via) > 0 && !mcp.SameOrigin(via[0].URL, req.URL) {
				stripSecrets(req.Header, kind, creds)
			}
			return nil
		},
	}, nil
}
