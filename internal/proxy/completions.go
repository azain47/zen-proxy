package proxy

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// codexBaseInstructions is a bundled copy of Codex's canonical agent prompt
// from openai/codex codex-rs/models-manager/prompt.md (Apache-2.0). It is
// served in every ModelInfo entry so Codex uses its real system prompt when
// routing through zen-proxy, rather than a stub that produces bogus tool
// calls.
//
//go:embed codex_prompt.md
var codexBaseInstructions string

func handleCompletions(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, 400, "failed to read request body")
			return
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeError(w, 400, "invalid request body")
			return
		}
		if req == nil {
			writeError(w, 400, "request body must be a JSON object")
			return
		}

		model, _ := req["model"].(string)
		if model == "" {
			req["model"] = cfg.Model
			model = cfg.Model
		}
		if cfg.Debugger != nil {
			cfg.Debugger.SetRequest(r.Context(), "chat-completions", model)
		}

		originalStream, _ := req["stream"].(bool)
		var toolRenames map[string]string
		if cfg.FreeTierClient {
			req["stream"] = true
			toolRenames = map[string]string{}
			shapePassthroughTools(req, toolRenames)
			ensurePassthroughEnv(req)
			if len(toolRenames) == 0 {
				toolRenames = nil
			}
		}
		if cfg.Provider == providerZen {
			sanitizePassthroughToolSchemas(req)
		}

		payload, _ := json.Marshal(req)
		upstream, err := http.NewRequestWithContext(r.Context(), "POST", cfg.Upstream, bytes.NewReader(payload))
		if err != nil {
			writeError(w, 502, "upstream error: "+err.Error())
			return
		}
		upstream.Header.Set("Content-Type", "application/json")
		setUpstreamHeaders(upstream, cfg)

		resp, err := executeUpstream(r.Context(), cfg, upstream, payload)
		if err != nil {
			writeError(w, 502, "upstream error: "+err.Error())
			return
		}
		defer resp.Body.Close()

		if cfg.FreeTierClient && resp.StatusCode == http.StatusOK {
			if !originalStream {
				completion, aggErr := aggregateSSECompletion(resp.Body, toolRenames)
				if aggErr != nil {
					writeError(w, 502, "aggregating upstream stream: "+aggErr.Error())
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(completion)
				return
			}
			if len(toolRenames) > 0 {
				for k, vals := range resp.Header {
					if strings.EqualFold(k, "Content-Length") {
						continue
					}
					for _, v := range vals {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(resp.StatusCode)
				copyStreamWithToolRenames(w, resp.Body, toolRenames)
				return
			}
		}

		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

func handleModels(models []ModelInfo, codexInstructions string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := make([]map[string]any, len(models))
		for i, m := range models {
			data[i] = map[string]any{
				"id":       m.ID,
				"object":   m.Object,
				"owned_by": m.OwnedBy,
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   data,
			"models": codexModelCatalog(models, codexInstructions),
		})
	}
}

func codexModelCatalog(models []ModelInfo, baseInstructions string) []map[string]any {
	catalog := make([]map[string]any, 0, len(models))
	for i, model := range models {
		if model.ID == "" {
			continue
		}
		contextWindow := model.ContextLength
		if contextWindow <= 0 {
			contextWindow = defaultCodexContextWindow
		}
		entry := map[string]any{
			"slug":                                 model.ID,
			"display_name":                         model.ID,
			"description":                          "Model routed through zen-proxy.",
			"base_instructions":                    baseInstructions,
			"shell_type":                           "shell_command",
			"visibility":                           "list",
			"supported_in_api":                     true,
			"priority":                             1000 + i,
			"additional_speed_tiers":               []any{},
			"service_tiers":                        []any{},
			"availability_nux":                     nil,
			"upgrade":                              nil,
			"supports_reasoning_summary_parameter": false,
			"supports_reasoning_summaries":         false,
			"default_reasoning_summary":            "none",
			"support_verbosity":                    false,
			"default_verbosity":                    "medium",
			"web_search_tool_type":                 "text",
			"truncation_policy":                    map[string]any{"mode": "tokens", "limit": 10000},
			"supports_parallel_tool_calls":         model.Capabilities != nil && model.Capabilities.SupportsParallelFunctionCalling,
			"supports_image_detail_original":       false,
			"context_window":                       contextWindow,
			"max_context_window":                   contextWindow,
			"effective_context_window_percent":     90,
			"experimental_supported_tools":         []any{},
			"input_modalities":                     codexInputModalities(model.Capabilities),
			"supports_search_tool":                 false,
			"use_responses_lite":                   false,
		}
		if model.Capabilities == nil || !model.Capabilities.ReasoningSupportKnown || model.Capabilities.SupportsReasoning {
			levels := codexReasoningLevels(model.Capabilities)
			entry["default_reasoning_level"] = "medium"
			entry["supported_reasoning_levels"] = levels
		} else {
			entry["supported_reasoning_levels"] = []any{}
		}
		catalog = append(catalog, entry)
	}
	return catalog
}

const defaultCodexContextWindow = 128000

func codexReasoningLevels(capabilities *ModelCapabilities) []map[string]string {
	levels := []map[string]string{
		{"effort": "low", "description": "Fast responses with lighter reasoning"},
		{"effort": "medium", "description": "Balances speed and reasoning depth for everyday tasks"},
		{"effort": "high", "description": "Greater reasoning depth for complex problems"},
	}
	if capabilities != nil && capabilities.SupportsMinimalReasoningEffort {
		levels = append([]map[string]string{{"effort": "minimal", "description": "Fastest responses with minimal reasoning"}}, levels...)
	}
	if capabilities != nil && capabilities.SupportsXHighReasoningEffort {
		levels = append(levels, map[string]string{"effort": "xhigh", "description": "Extra high reasoning depth for complex problems"})
	}
	return levels
}

func codexInputModalities(capabilities *ModelCapabilities) []string {
	if capabilities != nil && capabilities.SupportsVision {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// sanitizePassthroughToolSchemas applies the Zen tool-schema sanitizer to a
// raw passthrough request body (see sanitizeToolSchemasForZen).
func sanitizePassthroughToolSchemas(req map[string]any) {
	tools, ok := req["tools"].([]any)
	if !ok {
		return
	}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if params, ok := fn["parameters"].(map[string]any); ok {
			stripUnsupportedSchemaKeys(params)
		}
	}
}

// shapePassthroughTools applies the free-tier "read" tool requirement to a
// raw passthrough request body: renames a read-like client tool to "read"
// (recording the original in renames) or injects a minimal read tool.
func shapePassthroughTools(req map[string]any, renames map[string]string) {
	readLike := func(name string) bool {
		lower := strings.ToLower(name)
		return lower == "read" || lower == "read_file" || lower == "readfile" || lower == "read_text" || lower == "cat"
	}
	if rawTools, ok := req["tools"].([]any); ok {
		for _, raw := range rawTools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tool["function"].(map[string]any)
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			if name == "read" {
				return
			}
			if readLike(name) {
				renames["read"] = name
				fn["name"] = "read"
				return
			}
		}
	}
	injected := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "read",
			"description": "Read the contents of a file from the local filesystem.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "Absolute path to the file"},
					"offset": map[string]any{"type": "integer", "description": "Line offset to start from"},
					"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read"},
				},
				"required": []string{"path"},
			},
		},
	}
	if existing, ok := req["tools"].([]any); ok {
		req["tools"] = append(existing, injected)
	} else {
		req["tools"] = []any{injected}
	}
}

// ensurePassthroughEnv guarantees a raw passthrough request body carries the
// <env> block in its system message, which the free-tier gate requires.
func ensurePassthroughEnv(req map[string]any) {
	env := openCodeEnvBlock()
	rawMessages, _ := req["messages"].([]any)
	if len(rawMessages) > 0 {
		if first, ok := rawMessages[0].(map[string]any); ok {
			if role, _ := first["role"].(string); role == "system" {
				switch content := first["content"].(type) {
				case string:
					first["content"] = content + "\n\n" + env
					return
				case []any:
					first["content"] = append(content, map[string]any{"type": "text", "text": env})
					return
				}
			}
		}
	}
	system := map[string]any{"role": "system", "content": env}
	req["messages"] = append([]any{system}, rawMessages...)
}

// copyStreamWithToolRenames relays an SSE stream to the client, restoring
// renamed tool names inside tool_calls deltas.
func copyStreamWithToolRenames(w http.ResponseWriter, body io.Reader, renames map[string]string) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			io.WriteString(w, scanner.Text()+"\n")
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			io.WriteString(w, "data: [DONE]\n\n")
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			io.WriteString(w, scanner.Text()+"\n")
			continue
		}
		for i := range chunk.Choices {
			for j := range chunk.Choices[i].Delta.ToolCalls {
				tc := &chunk.Choices[i].Delta.ToolCalls[j]
				tc.Function.Name = restoreToolName(renames, tc.Function.Name)
			}
		}
		payload, err := json.Marshal(chunk)
		if err != nil {
			io.WriteString(w, scanner.Text()+"\n")
			continue
		}
		io.WriteString(w, "data: "+string(payload)+"\n\n")
	}
}
