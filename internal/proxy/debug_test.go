package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDebugTraceCapturesTranslatedRequestAndRedactsCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-upstream" {
			t.Fatalf("upstream authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		if payload["model"] != "selected-model" || payload["reasoning_effort"] != "low" {
			t.Fatalf("translated upstream payload = %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"cmpl_1","model":"selected-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	debugger := NewDebugger(false)
	cfg := Config{Model: "fallback", Upstream: upstream.URL, APIKey: "secret-upstream", Debugger: debugger}
	handler := withDebug(handleResponses(cfg), debugger)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"selected-model",
		"input":"hello",
		"reasoning":{"effort":"low"},
		"api_key":"secret-inbound"
	}`))
	req.Header.Set("Authorization", "Bearer secret-client")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	traces := debugger.Snapshot()
	if len(traces) != 1 {
		t.Fatalf("trace count = %d, want 1", len(traces))
	}
	trace := traces[0]
	if trace.Protocol != "openai-responses" || trace.Model != "selected-model" {
		t.Fatalf("trace route = %#v", trace)
	}
	if trace.Inbound.Headers["Authorization"] != "[redacted]" || strings.Contains(trace.Inbound.Body, "secret-inbound") {
		t.Fatalf("inbound trace was not redacted: %#v", trace.Inbound)
	}
	if trace.Upstream.Headers["Authorization"] != "[redacted]" || !strings.Contains(trace.Upstream.Body, `"reasoning_effort": "low"`) {
		t.Fatalf("upstream trace = %#v", trace.Upstream)
	}
	if trace.UpstreamResponse.Status != http.StatusOK || trace.Response.Status != http.StatusOK {
		t.Fatalf("trace statuses = upstream %d response %d", trace.UpstreamResponse.Status, trace.Response.Status)
	}
}

func TestParseRunOptions(t *testing.T) {
	t.Setenv("ZEN_VERBOSE", "")
	t.Setenv("ZEN_TUI", "")

	options, command, err := parseRunOptions([]string{"--verbose", "--tui"})
	if err != nil || command != "" || !options.Verbose || !options.TUI {
		t.Fatalf("options = %#v, command = %q, err = %v", options, command, err)
	}

	_, command, err = parseRunOptions([]string{"--version"})
	if err != nil || command != "version" {
		t.Fatalf("version command = %q, err = %v", command, err)
	}

	options, command, err = parseRunOptions([]string{"--debug"})
	if err != nil || command != "" || !options.Verbose {
		t.Fatalf("debug options = %#v, command = %q, err = %v", options, command, err)
	}
}

func TestSanitizeBodyRedactsNestedSecrets(t *testing.T) {
	body, truncated := sanitizeBody([]byte(`{"access_token":"top-secret","nested":{"api_key":"also-secret"}}`), false)
	if truncated || strings.Contains(body, "secret") || strings.Count(body, "[redacted]") != 2 {
		t.Fatalf("sanitized body = %q, truncated = %v", body, truncated)
	}
}
