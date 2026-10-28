package mcphub

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

var nameRE = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func publicName(server, tool string, used map[string]struct{}) string {
	base := ident(server) + "__" + ident(tool)
	if len(base) > 64 {
		sum := sha256.Sum256([]byte(server + "\n" + tool))
		base = base[:48] + "_" + hex.EncodeToString(sum[:])[:15]
	}
	name := base
	for n := 2; ; n++ {
		if _, taken := used[name]; !taken && name != "web_search" && name != "fetch_url" {
			used[name] = struct{}{}
			return name
		}
		suffix := "_" + strconv.Itoa(n)
		cut := base
		if len(cut)+len(suffix) > 64 {
			cut = cut[:64-len(suffix)]
		}
		name = cut + suffix
	}
}

func ident(value string) string {
	value = nameRE.ReplaceAllString(strings.TrimSpace(value), "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "x"
	}
	if value[0] >= '0' && value[0] <= '9' {
		value = "n" + value
	}
	return value
}
