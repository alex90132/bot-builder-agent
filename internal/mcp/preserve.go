package mcp

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
)

// PreserveURL оставляет путь и query как их прислали.
// Фрагмент отбрасывается: HTTP его не передаёт.
// Логин в адресе не принимается, его задают отдельно как Basic Auth.
func PreserveURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("пустой адрес")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("не разобрать адрес: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("нужна схема http или https")
	}
	if u.Host == "" {
		return "", fmt.Errorf("нет хоста")
	}
	if u.User != nil {
		return "", fmt.Errorf("логин и пароль укажите отдельно, не в адресе")
	}
	u.Fragment = ""
	return u.String(), nil
}

// DisplayURL показывает путь, а значения query заменяет. Секрет в параметре не должен попасть в чат.
func DisplayURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	q := u.Query()
	parts := make([]string, 0, len(q))
	for key := range q {
		parts = append(parts, key+"=…")
	}
	sort.Strings(parts)
	u.RawQuery = strings.Join(parts, "&")
	u.Fragment = ""
	return u.String()
}

// SameOrigin сравнивает схему и хост.
func SameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

// IsPrivateURL сообщает, что адрес смотрит на локальную или частную сеть.
func IsPrivateURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
