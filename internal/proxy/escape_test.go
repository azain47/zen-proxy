package proxy

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func assertNoHTMLEscapes(t *testing.T, label, out string) {
	t.Helper()
	for _, escape := range []string{`\u0026`, `\u003c`, `\u003e`} {
		if strings.Contains(out, escape) {
			t.Fatalf("%s emitted HTML escape %s: %q", label, escape, out)
		}
	}
}

func TestSanitizeBodyKeepsHTMLCharactersLiteral(t *testing.T) {
	out, _ := sanitizeBody([]byte(`{"cmd":"go test ./... && echo a > b < c"}`), false)
	assertNoHTMLEscapes(t, "sanitizeBody", out)
	if !strings.Contains(out, "&& echo a > b < c") {
		t.Fatalf("sanitizeBody lost literal characters: %q", out)
	}
}

func TestWriteSSEDoesNotHTMLEscape(t *testing.T) {
	rr := httptest.NewRecorder()
	writeSSE(rr, "content_block_delta", map[string]string{"text": "a && b > c < d"})
	out := rr.Body.String()
	assertNoHTMLEscapes(t, "writeSSE", out)
	if !strings.Contains(out, "a && b > c < d") {
		t.Fatalf("writeSSE lost literal characters: %q", out)
	}
}

func TestJSONTreeValuesKeepHTMLCharactersLiteral(t *testing.T) {
	root, err := parseJSONTree(`{"cmd & flag":"a && b > c < d"}`)
	if err != nil {
		t.Fatalf("parseJSONTree() error = %v", err)
	}
	pretty := prettyJSONTree(root)
	assertNoHTMLEscapes(t, "prettyJSONTree", pretty)
	if !strings.Contains(pretty, "a && b > c < d") || !strings.Contains(pretty, "cmd & flag") {
		t.Fatalf("prettyJSONTree lost literal characters: %q", pretty)
	}
	if raw := root.RawJSON(); strings.Contains(raw, `\u0026`) {
		t.Fatalf("RawJSON escaped HTML: %q", raw)
	}
}
