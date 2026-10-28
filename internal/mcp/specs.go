package mcp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"bot-builder-agent/internal/text"
)

const BuilderName = "botcraft-builder"

// Spec — одна запись из блока mcpServers или голый токен конструктора.
type Spec struct {
	Name     string
	URL      string
	Builder  bool
	AuthKind string
	Creds    Credentials
}

// ParseServers разбирает голый mcp_… или весь объект mcpServers.
// Адрес чужого сервера не нормализуется: путь и query остаются.
// Голый токен конструктора возвращается без адреса, его допишет вызывающий код.
func ParseServers(raw string) ([]Spec, bool) {
	raw = stripFence(strings.TrimSpace(raw))
	if raw == "" {
		return nil, false
	}
	if text.IsMCPToken(raw) {
		return []Spec{builderToken(raw)}, true
	}
	if token, ok := bearerValue(raw); ok {
		return []Spec{builderToken(token)}, true
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw[start:end+1]), &doc); err != nil {
		return nil, false
	}
	if serversRaw, ok := doc["mcpServers"]; ok {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(serversRaw, &servers); err != nil || len(servers) == 0 {
			return nil, false
		}
		out := make([]Spec, 0, len(servers))
		for name, rawServer := range servers {
			spec, ok := specFromServer(name, rawServer)
			if !ok {
				return nil, false
			}
			out = append(out, spec)
		}
		return out, true
	}
	spec, ok := specFromServer("mcp", json.RawMessage(raw[start:end+1]))
	if !ok {
		return nil, false
	}
	return []Spec{spec}, true
}

func builderToken(token string) Spec {
	return Spec{
		Name:     BuilderName,
		Builder:  true,
		AuthKind: AuthBearer,
		Creds:    Credentials{Token: token},
	}
}

func specFromServer(name string, raw json.RawMessage) (Spec, bool) {
	var body struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Spec{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Spec{}, false
	}
	spec := Spec{Name: name, AuthKind: AuthNone}
	if strings.TrimSpace(body.URL) != "" {
		preserved, err := PreserveURL(body.URL)
		if err != nil {
			return Spec{}, false
		}
		spec.URL = preserved
	}
	kind, creds := credsFromHeaders(body.Headers)
	spec.AuthKind = kind
	spec.Creds = creds
	if strings.EqualFold(name, BuilderName) || text.IsMCPToken(creds.Token) {
		spec.Builder = true
	}
	if spec.URL == "" && !spec.Builder {
		return Spec{}, false
	}
	return spec, true
}

func credsFromHeaders(headers map[string]string) (string, Credentials) {
	if len(headers) == 0 {
		return AuthNone, Credentials{}
	}
	var creds Credentials
	extra := map[string]string{}
	var auth string
	for key, value := range headers {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if strings.EqualFold(key, "Authorization") {
			auth = strings.TrimSpace(value)
			continue
		}
		extra[key] = value
	}
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		creds.Token = strings.TrimSpace(auth[7:])
		creds.Headers = extra
		if creds.Token == "" {
			return AuthNone, Credentials{}
		}
		return AuthBearer, creds
	}
	if strings.HasPrefix(strings.ToLower(auth), "basic ") {
		user, pass, ok := decodeBasic(auth)
		if !ok {
			return AuthNone, Credentials{}
		}
		creds.Username = user
		creds.Password = pass
		creds.Headers = extra
		return AuthBasic, creds
	}
	if auth != "" {
		extra["Authorization"] = auth
	}
	if len(extra) == 1 {
		for key, value := range extra {
			creds.Header = key
			creds.Token = value
		}
		return AuthAPIKey, creds
	}
	if len(extra) > 1 {
		creds.Headers = extra
		return AuthHeaders, creds
	}
	return AuthNone, Credentials{}
}

func decodeBasic(value string) (string, string, bool) {
	raw := strings.TrimSpace(value)
	if len(raw) < 6 || !strings.EqualFold(raw[:6], "basic ") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw[6:]))
	if err != nil {
		return "", "", false
	}
	user, pass, ok := strings.Cut(string(decoded), ":")
	if !ok || user == "" {
		return "", "", false
	}
	return user, pass, true
}

// HeaderClone копирует заголовки, чтобы чужой запрос их не увидел.
func HeaderClone(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}
