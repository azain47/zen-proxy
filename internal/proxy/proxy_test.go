package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCompletionsRejectsInvalidJSON(t *testing.T) {
	cfg := Config{Model: "fallback-model", Upstream: "http://invalid.local"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	rr := httptest.NewRecorder()

	handleCompletions(cfg).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHandleCompletionsRejectsNullBody(t *testing.T) {
	cfg := Config{Model: "fallback-model", Upstream: "http://invalid.local"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("null"))
	rr := httptest.NewRecorder()

	handleCompletions(cfg).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestLoadConfigDefaultsToLocalhost(t *testing.T) {
	t.Setenv("ZEN_HOST", "")
	t.Setenv("ZEN_PORT", "")
	t.Setenv("ZEN_PROVIDER", "")

	cfg := LoadConfig()

	if cfg.Host != "127.0.0.1" {
		t.Fatalf("host = %q, want 127.0.0.1", cfg.Host)
	}
	if cfg.Port != "8788" {
		t.Fatalf("port = %q, want 8788", cfg.Port)
	}
}

func TestLoadConfigAllowsHostOverride(t *testing.T) {
	t.Setenv("ZEN_HOST", "0.0.0.0")

	cfg := LoadConfig()

	if cfg.Host != "0.0.0.0" {
		t.Fatalf("host = %q, want 0.0.0.0", cfg.Host)
	}
}

func TestOpenRouterPresetFromEnv(t *testing.T) {
	t.Setenv("ZEN_PROVIDER", "openrouter")
	t.Setenv("ZEN_HOST", "")
	t.Setenv("ZEN_PORT", "")
	t.Setenv("ZEN_UPSTREAM", "")
	t.Setenv("ZEN_MODELS_URL", "")
	t.Setenv("ZEN_API_KEY", "")
	t.Setenv("ZEN_MODEL", "")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")

	cfg := LoadConfig()

	if cfg.Provider != providerOpenRouter {
		t.Fatalf("provider = %q, want %q", cfg.Provider, providerOpenRouter)
	}
	if cfg.Upstream != defaultOpenRouterUpstream {
		t.Fatalf("upstream = %q, want %q", cfg.Upstream, defaultOpenRouterUpstream)
	}
	if cfg.ModelsURL != defaultOpenRouterModels {
		t.Fatalf("models URL = %q, want %q", cfg.ModelsURL, defaultOpenRouterModels)
	}
	if cfg.APIKey != "sk-or-test" {
		t.Fatalf("API key = %q, want OPENROUTER_API_KEY", cfg.APIKey)
	}
	if cfg.Model != "openrouter/free" {
		t.Fatalf("model = %q, want openrouter/free", cfg.Model)
	}
}

func TestOpenRouterHeadersAndFallbackModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or-test" {
			t.Fatalf("Authorization = %q, want bearer key", got)
		}
		if got := r.Header.Get("HTTP-Referer"); got != "https://example.com" {
			t.Fatalf("HTTP-Referer = %q, want configured referer", got)
		}
		if got := r.Header.Get("X-Title"); got != "Zen Proxy Tests" {
			t.Fatalf("X-Title = %q, want configured title", got)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode upstream request: %v", err)
		}
		if payload["model"] != "openrouter/free" {
			t.Fatalf("upstream model = %v, want openrouter/free", payload["model"])
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"cmpl_1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	cfg := Config{
		Provider:    providerOpenRouter,
		Upstream:    upstream.URL,
		APIKey:      "sk-or-test",
		Model:       "openrouter/free",
		HTTPReferer: "https://example.com",
		AppTitle:    "Zen Proxy Tests",
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	rr := httptest.NewRecorder()

	handleCompletions(cfg).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestFreeModelDetectionSupportsOpenRouter(t *testing.T) {
	cases := []struct {
		name string
		info ModelInfo
		want bool
	}{
		{name: "zen suffix", info: ModelInfo{ID: "deepseek-v4-flash-free"}, want: true},
		{name: "openrouter suffix", info: ModelInfo{ID: "qwen/qwen3-coder:free"}, want: true},
		{name: "openrouter router", info: ModelInfo{ID: "openrouter/free"}, want: true},
		{name: "zero pricing", info: ModelInfo{ID: "provider/model", Pricing: &ModelPricing{Prompt: "0", Completion: "0"}}, want: true},
		{name: "paid pricing", info: ModelInfo{ID: "provider/model", Pricing: &ModelPricing{Prompt: "0.000001", Completion: "0"}}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFreeModel(tc.info); got != tc.want {
				t.Fatalf("isFreeModel(%+v) = %v, want %v", tc.info, got, tc.want)
			}
		})
	}
}

func TestTextOnlyOutputFilter(t *testing.T) {
	cases := []struct {
		name string
		info ModelInfo
		want bool
	}{
		{name: "unknown architecture", info: ModelInfo{ID: "model"}, want: true},
		{name: "text output", info: ModelInfo{ID: "model", Architecture: &ModelArchitecture{OutputModalities: []string{"text"}}}, want: true},
		{name: "vision input text output", info: ModelInfo{ID: "model", Architecture: &ModelArchitecture{OutputModalities: []string{"TEXT"}}}, want: true},
		{name: "audio output", info: ModelInfo{ID: "model", Architecture: &ModelArchitecture{OutputModalities: []string{"text", "audio"}}}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasTextOnlyOutput(tc.info); got != tc.want {
				t.Fatalf("hasTextOnlyOutput(%+v) = %v, want %v", tc.info, got, tc.want)
			}
		})
	}
}

func TestAnthropicNonStreamingRejectsMissingChoices(t *testing.T) {
	rr := httptest.NewRecorder()

	anthropicNonStreaming(rr, strings.NewReader(`{"id":"cmpl_1","choices":[]}`), "fallback-model")

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
}

func TestResponsesNonStreamingRejectsMissingChoices(t *testing.T) {
	rr := httptest.NewRecorder()

	responsesNonStreaming(rr, strings.NewReader(`{"id":"cmpl_1","choices":[]}`), "fallback-model")

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadGateway)
	}
}

func TestAnthropicUsesFallbackModelWhenRequestOmitsModel(t *testing.T) {
	const fallbackModel = "fallback-model"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode upstream request: %v", err)
		}
		if payload["model"] != fallbackModel {
			t.Fatalf("upstream model = %v, want %q", payload["model"], fallbackModel)
		}

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"id":"cmpl_1",
			"model":"fallback-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
		}`)
	}))
	defer upstream.Close()

	cfg := Config{Model: fallbackModel, Upstream: upstream.URL, APIKey: "test"}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"messages":[{"role":"user","content":"hi"}],
		"max_tokens":32
	}`))
	rr := httptest.NewRecorder()

	handleAnthropic(cfg).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp anthropicResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Model != fallbackModel {
		t.Fatalf("response model = %q, want %q", resp.Model, fallbackModel)
	}
}

func TestAnthropicStreamingHandlesMultipleToolCalls(t *testing.T) {
	body := strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"first_tool","arguments":"{\"a\""}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"second_tool","arguments":"{\"b\""}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":":1}"}},{"index":1,"type":"function","function":{"arguments":":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"completion_tokens":12}}`,
		`data: [DONE]`,
		``,
	}, "\n\n"))
	rr := httptest.NewRecorder()

	anthropicStreaming(rr, body, "test-model")

	out := rr.Body.String()
	if strings.Count(out, `"type":"tool_use"`) != 2 {
		t.Fatalf("tool_use blocks = %d, want 2\n%s", strings.Count(out, `"type":"tool_use"`), out)
	}
	if !strings.Contains(out, `"index":0`) || !strings.Contains(out, `"index":1`) {
		t.Fatalf("expected separate tool block indexes\n%s", out)
	}
	if !strings.Contains(out, `"partial_json":"{\"a\""`) || !strings.Contains(out, `"partial_json":":1}"`) {
		t.Fatalf("missing first tool argument deltas\n%s", out)
	}
	if !strings.Contains(out, `"partial_json":"{\"b\""`) || !strings.Contains(out, `"partial_json":":2}"`) {
		t.Fatalf("missing second tool argument deltas\n%s", out)
	}
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("missing tool_use stop reason\n%s", out)
	}
}

func TestResponsesStreamingHandlesMultipleToolCalls(t *testing.T) {
	body := strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"first_tool","arguments":"{\"a\""}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"second_tool","arguments":"{\"b\""}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":":1}"}},{"index":1,"type":"function","function":{"arguments":":2}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":12,"total_tokens":15}}`,
		`data: [DONE]`,
		``,
	}, "\n\n"))
	rr := httptest.NewRecorder()

	responsesStreaming(rr, body, "test-model")

	out := rr.Body.String()
	if strings.Count(out, `"type":"function_call"`) != 6 {
		t.Fatalf("function_call mentions = %d, want 6\n%s", strings.Count(out, `"type":"function_call"`), out)
	}
	if !strings.Contains(out, `"output_index":0`) || !strings.Contains(out, `"output_index":1`) {
		t.Fatalf("expected separate tool output indexes\n%s", out)
	}
	if !strings.Contains(out, `"arguments":"{\"a\":1}"`) || !strings.Contains(out, `"arguments":"{\"b\":2}"`) {
		t.Fatalf("missing completed tool arguments\n%s", out)
	}
	if !strings.Contains(out, `event: response.completed`) {
		t.Fatalf("missing response.completed\n%s", out)
	}
}

func TestStreamingPropagatesUpstreamErrorEvents(t *testing.T) {
	body := strings.NewReader(`data: {"error":{"type":"rate_limit_error","message":"slow down"}}` + "\n\n")
	rr := httptest.NewRecorder()

	responsesStreaming(rr, body, "test-model")

	out := rr.Body.String()
	if !strings.Contains(out, `event: response.failed`) {
		t.Fatalf("missing response.failed\n%s", out)
	}
	if !strings.Contains(out, `slow down`) {
		t.Fatalf("missing upstream error message\n%s", out)
	}
}

func TestTranslatedUsagePreservesCacheDetails(t *testing.T) {
	completion := `{
		"id":"cmpl_cache",
		"model":"test-model",
		"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{
			"prompt_tokens":32092,
			"completion_tokens":16,
			"total_tokens":32108,
			"prompt_cache_hit_tokens":32000,
			"prompt_cache_miss_tokens":92,
			"prompt_tokens_details":{"cached_tokens":32000},
			"completion_tokens_details":{"reasoning_tokens":16}
		}
	}`

	anthropicRR := httptest.NewRecorder()
	anthropicNonStreaming(anthropicRR, strings.NewReader(completion), "fallback-model")
	if anthropicRR.Code != http.StatusOK {
		t.Fatalf("anthropic status = %d, body = %s", anthropicRR.Code, anthropicRR.Body.String())
	}
	var anthropic map[string]any
	if err := json.NewDecoder(anthropicRR.Body).Decode(&anthropic); err != nil {
		t.Fatalf("failed to decode anthropic response: %v", err)
	}
	anthropicUsage := anthropic["usage"].(map[string]any)
	if anthropicUsage["cache_read_input_tokens"].(float64) != 32000 {
		t.Fatalf("anthropic cache_read_input_tokens = %v, want 32000", anthropicUsage["cache_read_input_tokens"])
	}
	if anthropicUsage["prompt_cache_miss_tokens"].(float64) != 92 {
		t.Fatalf("anthropic prompt_cache_miss_tokens = %v, want 92", anthropicUsage["prompt_cache_miss_tokens"])
	}

	responsesRR := httptest.NewRecorder()
	responsesNonStreaming(responsesRR, strings.NewReader(completion), "fallback-model")
	if responsesRR.Code != http.StatusOK {
		t.Fatalf("responses status = %d, body = %s", responsesRR.Code, responsesRR.Body.String())
	}
	var responses map[string]any
	if err := json.NewDecoder(responsesRR.Body).Decode(&responses); err != nil {
		t.Fatalf("failed to decode responses response: %v", err)
	}
	responsesUsage := responses["usage"].(map[string]any)
	inputDetails := responsesUsage["input_tokens_details"].(map[string]any)
	if inputDetails["cached_tokens"].(float64) != 32000 {
		t.Fatalf("responses cached_tokens = %v, want 32000", inputDetails["cached_tokens"])
	}
	outputDetails := responsesUsage["output_tokens_details"].(map[string]any)
	if outputDetails["reasoning_tokens"].(float64) != 16 {
		t.Fatalf("responses reasoning_tokens = %v, want 16", outputDetails["reasoning_tokens"])
	}
}
