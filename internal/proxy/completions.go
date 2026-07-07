package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

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
		}

		payload, _ := json.Marshal(req)
		upstream, err := http.NewRequest("POST", cfg.Upstream, bytes.NewReader(payload))
		if err != nil {
			writeError(w, 502, "upstream error: "+err.Error())
			return
		}
		upstream.Header.Set("Content-Type", "application/json")
		setUpstreamHeaders(upstream, cfg)

		resp, err := http.DefaultClient.Do(upstream)
		if err != nil {
			writeError(w, 502, "upstream error: "+err.Error())
			return
		}
		defer resp.Body.Close()

		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

func handleModels(models []ModelInfo) http.HandlerFunc {
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
			"models": codexModelCatalog(models),
		})
	}
}

func codexModelCatalog(models []ModelInfo) []map[string]any {
	catalog := make([]map[string]any, 0, len(models))
	for i, model := range models {
		if model.ID == "" {
			continue
		}
		contextWindow := model.ContextLength
		if contextWindow <= 0 {
			contextWindow = 128000
		}
		catalog = append(catalog, map[string]any{
			"slug":                             model.ID,
			"display_name":                     model.ID,
			"description":                      "Model routed through zen-proxy.",
			"base_instructions":                "You are Codex, a coding agent. Help the user complete software engineering tasks in the current workspace.",
			"default_reasoning_level":          "none",
			"supported_reasoning_levels":       []any{},
			"shell_type":                       "shell_command",
			"visibility":                       "list",
			"supported_in_api":                 true,
			"priority":                         1000 + i,
			"additional_speed_tiers":           []any{},
			"service_tiers":                    []any{},
			"availability_nux":                 nil,
			"upgrade":                          nil,
			"supports_reasoning_summaries":     false,
			"default_reasoning_summary":        "none",
			"support_verbosity":                false,
			"default_verbosity":                "medium",
			"apply_patch_tool_type":            "freeform",
			"web_search_tool_type":             "text_and_image",
			"truncation_policy":                map[string]any{"mode": "tokens", "limit": 10000},
			"supports_parallel_tool_calls":     true,
			"supports_image_detail_original":   false,
			"context_window":                   contextWindow,
			"max_context_window":               contextWindow,
			"effective_context_window_percent": 90,
			"experimental_supported_tools":     []any{},
			"input_modalities":                 []string{"text"},
			"supports_search_tool":             false,
			"use_responses_lite":               false,
		})
	}
	return catalog
}
