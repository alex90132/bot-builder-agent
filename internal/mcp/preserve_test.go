package mcp

import (
	"strings"
	"testing"
)

func TestPreserveURLKeepsPathAndQuery(t *testing.T) {
	got, err := PreserveURL("https://example.com/custom/mcp?tenant=acme&x=1#frag")
	if err != nil || got != "https://example.com/custom/mcp?tenant=acme&x=1" {
		t.Fatalf("%q %v", got, err)
	}
	shown := DisplayURL("https://notes.example.com/mcp?token=secret&x=1")
	if strings.Contains(shown, "secret") || !strings.Contains(shown, "token=…") {
		t.Fatal(shown)
	}
	if _, err := PreserveURL("https://user:pass@example.com/mcp"); err == nil {
		t.Fatal("логин в адресе должен отвергаться")
	}
	norm, err := NormalizeURL("https://example.com")
	if err != nil || norm != "https://example.com/mcp" {
		t.Fatalf("конструктор %q %v", norm, err)
	}
}

func TestParseServersKeepsCustomURL(t *testing.T) {
	raw := `{
	  "mcpServers": {
	    "notes": {"url": "https://notes.example.com/custom?x=1"},
	    "docs": {"url": "https://docs.example.com/mcp", "headers": {"Authorization": "Bearer plain-token"}},
	    "search": {"url": "https://search.example.com/mcp", "headers": {"X-API-Key": "k"}}
	  }
	}`
	specs, ok := ParseServers(raw)
	if !ok || len(specs) != 3 {
		t.Fatalf("%v %+v", ok, specs)
	}
	byName := map[string]Spec{}
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	if byName["notes"].URL != "https://notes.example.com/custom?x=1" || byName["notes"].AuthKind != AuthNone || byName["notes"].Builder {
		t.Fatalf("notes %+v", byName["notes"])
	}
	if byName["docs"].AuthKind != AuthBearer || byName["docs"].Creds.Token != "plain-token" || byName["docs"].Builder {
		t.Fatalf("docs %+v", byName["docs"])
	}
	if byName["search"].AuthKind != AuthAPIKey || byName["search"].Creds.Header != "X-API-Key" || byName["search"].Creds.Token != "k" {
		t.Fatalf("search %+v", byName["search"])
	}
}
