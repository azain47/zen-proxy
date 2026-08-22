package proxy

import (
	"strings"
	"testing"
)

func TestPrettyJSONRendersUnicodeCharacters(t *testing.T) {
	formatted, ok := prettyJSON(`{"text":"\u4f60\u597d \ud83d\udc4b caf\u00e9"}`)
	if !ok || !strings.Contains(formatted, "你好 👋 café") || strings.Contains(formatted, `\u4f60`) {
		t.Fatalf("formatted JSON = %q, ok = %v", formatted, ok)
	}
}
