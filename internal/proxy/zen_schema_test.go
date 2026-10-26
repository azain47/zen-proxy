package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeToolSchema(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body struct {
		Tools []struct {
			Function struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Parameters  map[string]any `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding captured body: %v", err)
	}
	if len(body.Tools) == 0 {
		t.Fatal("no tools captured")
	}
	return body.Tools[0].Function.Parameters
}

func TestSanitizeToolSchemaDropsUnsupportedZenKeywords(t *testing.T) {
	raw := json.RawMessage(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"maximum": {"type": "string", "description": "a property literally named maximum"},
			"limit": {"type": "integer", "minimum": 1, "maximum": 2000, "exclusiveMinimum": 0},
			"code": {"type": "string", "pattern": "^[a-z]+$"},
			"files": {
				"anyOf": [
					{"type": "array", "items": {"type": "string", "minLength": 1}},
					{"type": "object", "propertyNames": {"pattern": "^[a-z]+$"}}
				]
			},
			"meta": {"$defs": {"inner": {"type": "number", "maximum": 10}}}
		},
		"required": ["limit"]
	}`)
	sanitized := string(sanitizeToolSchemaForZen(raw))
	for _, banned := range []string{`"$schema"`, `"minimum":1`, `"maximum":2000`, `"exclusiveMinimum"`, `"propertyNames"`, `"maximum":10`, `"pattern"`} {
		if strings.Contains(sanitized, banned) {
			t.Errorf("sanitized schema still contains %s: %s", banned, sanitized)
		}
	}
	for _, kept := range []string{`"maximum":{"description"`, `"minLength":1`, `"additionalProperties":false`, `"required":["limit"]`} {
		if !strings.Contains(sanitized, kept) {
			t.Errorf("sanitized schema lost %s: %s", kept, sanitized)
		}
	}
}

func TestSanitizeToolSchemaInvalidJSONPassesThrough(t *testing.T) {
	raw := json.RawMessage(`not json`)
	if got := string(sanitizeToolSchemaForZen(raw)); got != "not json" {
		t.Fatalf("invalid schema was modified: %q", got)
	}
}

func zenCaptureServer(t *testing.T, captured *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
}

func TestUpstreamRequestSanitizesZenToolSchemas(t *testing.T) {
	var captured []byte
	server := zenCaptureServer(t, &captured)
	defer server.Close()

	cfg := Config{
		Provider:        providerZen,
		Upstream:        server.URL,
		APIKey:          "public",
		Model:           "deepseek-v4.1-flash-free",
		FreeTierClient:  true,
		OpenCodeVersion: "1.19.4",
	}
	tools := []ChatTool{{
		Type: "function",
		Function: ChatFunction{
			Name:        "artifact",
			Description: "Create an artifact.",
			Parameters:  json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"count":{"type":"integer","minimum":1,"maximum":9}},"required":["count"]}`),
		},
	}}
	messages := []ChatMessage{{Role: "user", Content: jsonStr("hello")}}
	resp, _, err := upstreamRequest(context.Background(), cfg, cfg.Model, messages, true, tools, chatRequestOptions{})
	if err != nil {
		t.Fatalf("upstreamRequest: %v", err)
	}
	resp.Body.Close()

	params := decodeToolSchema(t, captured)
	if _, ok := params["$schema"]; ok {
		t.Fatal("$schema was not stripped for zen")
	}
	props, _ := params["properties"].(map[string]any)
	count, _ := props["count"].(map[string]any)
	if _, ok := count["minimum"]; ok {
		t.Fatal("minimum was not stripped for zen")
	}
	if _, ok := count["maximum"]; ok {
		t.Fatal("maximum was not stripped for zen")
	}
	if count["type"] != "integer" {
		t.Fatalf("count.type = %v", count["type"])
	}
}

func TestUpstreamRequestLeavesOpenRouterToolSchemasAlone(t *testing.T) {
	var captured []byte
	server := zenCaptureServer(t, &captured)
	defer server.Close()

	cfg := Config{
		Provider: providerOpenRouter,
		Upstream: server.URL,
		APIKey:   "test",
		Model:    "deepseek/deepseek-chat",
	}
	tools := []ChatTool{{
		Type: "function",
		Function: ChatFunction{
			Name:       "artifact",
			Parameters: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","minimum":1}}}`),
		},
	}}
	messages := []ChatMessage{{Role: "user", Content: jsonStr("hello")}}
	resp, _, err := upstreamRequest(context.Background(), cfg, cfg.Model, messages, true, tools, chatRequestOptions{})
	if err != nil {
		t.Fatalf("upstreamRequest: %v", err)
	}
	resp.Body.Close()

	params := decodeToolSchema(t, captured)
	props, _ := params["properties"].(map[string]any)
	count, _ := props["count"].(map[string]any)
	if _, ok := count["minimum"]; !ok {
		t.Fatal("minimum should be preserved for openrouter")
	}
}

func TestSanitizePassthroughToolSchemas(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "artifact",
					"parameters": map[string]any{
						"$schema": "https://json-schema.org/draft/2020-12/schema",
						"type":    "object",
						"properties": map[string]any{
							"count": map[string]any{"type": "integer", "minimum": 1, "maximum": 9},
						},
					},
				},
			},
		},
	}
	sanitizePassthroughToolSchemas(req)

	tools := req["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if _, ok := params["$schema"]; ok {
		t.Fatal("$schema was not stripped")
	}
	count := params["properties"].(map[string]any)["count"].(map[string]any)
	if _, ok := count["minimum"]; ok {
		t.Fatal("minimum was not stripped")
	}
	if _, ok := count["maximum"]; ok {
		t.Fatal("maximum was not stripped")
	}
}
