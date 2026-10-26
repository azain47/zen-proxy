package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	sessionIDPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	requestIDPattern = regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
)

func TestOpenCodeSessionIDShape(t *testing.T) {
	id := newOpenCodeSessionID()
	if !sessionIDPattern.MatchString(id) {
		t.Fatalf("session id %q does not match OpenCode shape", id)
	}

	head, err := parseIDHead(id[4:16])
	if err != nil {
		t.Fatalf("parsing session id head: %v", err)
	}
	if kind := head & 0xfff; kind != openCodeSessionKind {
		t.Fatalf("session id kind = %#x, want %#x", kind, openCodeSessionKind)
	}

	ms := openCodeSessionIDBase - (head >> 12)
	if drift := time.Since(time.UnixMilli(ms)); drift < -5*time.Second || drift > 5*time.Second {
		t.Fatalf("session id timestamp drifted by %s", drift)
	}
}

func TestOpenCodeRequestIDShape(t *testing.T) {
	id := newOpenCodeRequestID()
	if !requestIDPattern.MatchString(id) {
		t.Fatalf("request id %q does not match OpenCode shape", id)
	}

	head, err := parseIDHead(id[4:16])
	if err != nil {
		t.Fatalf("parsing request id head: %v", err)
	}
	if kind := head & 0xfff; kind != openCodeMessageKind {
		t.Fatalf("request id kind = %#x, want %#x", kind, openCodeMessageKind)
	}

	// The 48-bit head keeps only the low 36 timestamp bits, so compare modulo 2^36.
	want := time.Now().UnixMilli() & 0xfffffffff
	got := head >> 12
	if diff := want - got; diff < -5000 || diff > 5000 {
		t.Fatalf("request id timestamp off by %dms", diff)
	}
}

func parseIDHead(hex string) (int64, error) {
	return strconv.ParseInt(hex, 16, 64)
}

func TestEnsureSystemEnvPrepends(t *testing.T) {
	messages := []ChatMessage{{Role: "user", Content: jsonStr("hello")}}
	got := ensureSystemEnv(messages)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Role != "system" || !strings.Contains(contentString(t, got[0]), "<env>") {
		t.Fatalf("expected prepended env system message, got %#v", got[0])
	}
	if got[1].Role != "user" {
		t.Fatalf("original message not preserved: %#v", got[1])
	}
}

func TestEnsureSystemEnvAppends(t *testing.T) {
	messages := []ChatMessage{{Role: "system", Content: jsonStr("You are a test.")}, {Role: "user", Content: jsonStr("hello")}}
	got := ensureSystemEnv(messages)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	content := contentString(t, got[0])
	if !strings.Contains(content, "You are a test.") || !strings.Contains(content, "<env>") {
		t.Fatalf("system content missing original text or env block: %q", content)
	}
}

func TestEnsurePassthroughEnv(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a test."},
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	ensurePassthroughEnv(body)
	messages := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("len = %d, want 2", len(messages))
	}
	content, _ := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(content, "You are a test.") || !strings.Contains(content, "<env>") {
		t.Fatalf("system content missing original text or env block: %q", content)
	}

	empty := map[string]any{}
	ensurePassthroughEnv(empty)
	messages = empty["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("len = %d, want 1", len(messages))
	}
	if role, _ := messages[0].(map[string]any)["role"].(string); role != "system" {
		t.Fatalf("first role = %q, want system", role)
	}

	parts := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
		},
	}
	ensurePassthroughEnv(parts)
	contentParts, _ := parts["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(contentParts) != 2 {
		t.Fatalf("content parts = %d, want 2", len(contentParts))
	}
}

func TestUpstreamRequestSendsFreeTierIdentity(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	cfg := Config{
		Provider:        providerZen,
		Upstream:        server.URL,
		APIKey:          "public",
		Model:           "deepseek-v4.1-flash-free",
		FreeTierClient:  true,
		OpenCodeVersion: "1.19.4",
	}
	messages := []ChatMessage{{Role: "user", Content: jsonStr("hello")}}
	resp, _, err := upstreamRequest(context.Background(), cfg, cfg.Model, messages, true, nil, chatRequestOptions{})
	if err != nil {
		t.Fatalf("upstreamRequest: %v", err)
	}
	resp.Body.Close()

	if ua := captured.Header.Get("User-Agent"); ua != "opencode/1.19.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4" {
		t.Fatalf("User-Agent = %q", ua)
	}
	if captured.Header.Get("x-opencode-project") != "global" {
		t.Fatalf("x-opencode-project = %q", captured.Header.Get("x-opencode-project"))
	}
	session := captured.Header.Get("x-opencode-session")
	if session == "" || session != captured.Header.Get("x-opencode-session-id") || !sessionIDPattern.MatchString(session) {
		t.Fatalf("session headers invalid: %q / %q", session, captured.Header.Get("x-opencode-session-id"))
	}
	if request := captured.Header.Get("x-opencode-request"); !requestIDPattern.MatchString(request) {
		t.Fatalf("x-opencode-request = %q", request)
	}

	var body struct {
		Stream   bool `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(capturedBody, &body); err != nil {
		t.Fatalf("decoding captured body: %v", err)
	}
	if !body.Stream {
		t.Fatal("stream was not forced on")
	}
	if len(body.Messages) == 0 || body.Messages[0].Role != "system" || !strings.Contains(body.Messages[0].Content, "<env>") {
		t.Fatalf("system env block missing: %#v", body.Messages)
	}
	hasRead := false
	for _, tool := range body.Tools {
		if tool.Function.Name == "read" {
			hasRead = true
		}
	}
	if !hasRead {
		t.Fatalf("read tool missing from %#v", body.Tools)
	}
}

func contentString(t *testing.T, message ChatMessage) string {
	t.Helper()
	var content string
	if err := json.Unmarshal(message.Content, &content); err != nil {
		t.Fatalf("decoding content: %v", err)
	}
	return content
}
