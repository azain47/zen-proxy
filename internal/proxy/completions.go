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
		})
	}
}
