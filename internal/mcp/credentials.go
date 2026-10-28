package mcp

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	AuthNone    = "none"
	AuthBearer  = "bearer"
	AuthAPIKey  = "api_key"
	AuthBasic   = "basic"
	AuthHeaders = "headers"
	AuthOAuth   = "oauth"
)

// Credentials — секреты одного сервера. В модель, лог и чат их не кладут.
type Credentials struct {
	Token        string            `json:"token,omitempty"`
	Header       string            `json:"header,omitempty"`
	Username     string            `json:"username,omitempty"`
	Password     string            `json:"password,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	ClientID     string            `json:"client_id,omitempty"`
	ClientSecret string            `json:"client_secret,omitempty"`
	Scopes       []string          `json:"scopes,omitempty"`
	AuthURL      string            `json:"auth_url,omitempty"`
	TokenURL     string            `json:"token_url,omitempty"`
	Resource     string            `json:"resource,omitempty"`
	AccessToken  string            `json:"access_token,omitempty"`
	RefreshToken string            `json:"refresh_token,omitempty"`
	Expiry       time.Time         `json:"expiry,omitempty"`
}

// ParseCredentials читает JSON секретов. Пустая строка значит отсутствие секретов.
func ParseCredentials(raw string) (Credentials, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "null" {
		return Credentials{}, nil
	}
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

// JSON сериализует секреты для SQLite.
func (c Credentials) JSON() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Masked — короткая подпись без секрета.
func (c Credentials) Masked(kind string) string {
	switch kind {
	case AuthNone, "":
		return "без авторизации"
	case AuthBearer:
		return "Bearer " + mask(c.Token)
	case AuthAPIKey:
		name := c.Header
		if name == "" {
			name = "X-API-Key"
		}
		return name + " " + mask(c.Token)
	case AuthBasic:
		return "Basic " + mask(c.Username)
	case AuthHeaders:
		return "заголовки " + headerNames(c.Headers)
	case AuthOAuth:
		if c.AccessToken == "" && c.RefreshToken == "" {
			if c.ClientID == "" {
				return "OAuth, клиент ещё не зарегистрирован"
			}
			return "OAuth, клиент " + mask(c.ClientID)
		}
		return "OAuth " + mask(c.AccessToken)
	default:
		return "неизвестный способ"
	}
}

// Apply добавляет заголовки авторизации только в копию запроса.
func (c Credentials) Apply(kind string, header http.Header) {
	switch kind {
	case AuthBearer:
		if token := strings.TrimSpace(c.Token); token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
	case AuthAPIKey:
		name := strings.TrimSpace(c.Header)
		if name == "" {
			name = "X-API-Key"
		}
		if token := strings.TrimSpace(c.Token); token != "" {
			header.Set(name, token)
		}
	case AuthBasic:
		raw := base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password))
		header.Set("Authorization", "Basic "+raw)
	case AuthHeaders:
		for key, value := range c.Headers {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			header.Set(key, value)
		}
	case AuthOAuth:
		if token := strings.TrimSpace(c.AccessToken); token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
	}
	for key, value := range c.Headers {
		if kind == AuthHeaders {
			break
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.EqualFold(key, "Authorization") {
			continue
		}
		if header.Get(key) == "" {
			header.Set(key, value)
		}
	}
}

func mask(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "не задан"
	}
	if len(value) <= 8 {
		return "задан"
	}
	return value[:4] + "…" + value[len(value)-4:]
}

func headerNames(headers map[string]string) string {
	if len(headers) == 0 {
		return "не заданы"
	}
	names := make([]string, 0, len(headers))
	for key := range headers {
		names = append(names, key)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
