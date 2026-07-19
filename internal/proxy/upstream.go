package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var proxyHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

type chatRequestOptions struct {
	MaxTokens         int
	Temperature       *float64
	TopP              *float64
	Stop              []string
	ToolChoice        json.RawMessage
	ParallelToolCalls *bool
	ReasoningEffort   string
}

func upstreamRequest(ctx context.Context, cfg Config, model string, messages []ChatMessage, stream bool, tools []ChatTool, opts chatRequestOptions) (*http.Response, error) {
	body := map[string]any{
		"model":    effectiveModel(cfg, model),
		"messages": messages,
		"stream":   stream,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if opts.MaxTokens > 0 {
		body["max_tokens"] = opts.MaxTokens
	}
	if opts.Temperature != nil {
		body["temperature"] = *opts.Temperature
	}
	if opts.TopP != nil {
		body["top_p"] = *opts.TopP
	}
	if len(opts.Stop) > 0 {
		body["stop"] = opts.Stop
	}
	if len(opts.ToolChoice) > 0 && string(opts.ToolChoice) != "null" {
		body["tool_choice"] = opts.ToolChoice
	}
	if opts.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *opts.ParallelToolCalls
	}
	if opts.ReasoningEffort != "" {
		body["reasoning_effort"] = opts.ReasoningEffort
	}

	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Upstream, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	setUpstreamHeaders(req, cfg)

	return executeUpstream(ctx, cfg, req, payload)
}

func executeUpstream(ctx context.Context, cfg Config, req *http.Request, payload []byte) (*http.Response, error) {
	if cfg.Debugger != nil {
		cfg.Debugger.RecordUpstreamRequest(ctx, req, payload)
	}

	resp, err := proxyHTTPClient.Do(req)
	if err != nil {
		if cfg.Debugger != nil {
			cfg.Debugger.RecordUpstreamError(ctx, err)
		}
		return nil, err
	}
	if cfg.Debugger != nil {
		cfg.Debugger.RecordUpstreamResponse(ctx, resp)
		resp.Body = newCaptureReadCloser(resp.Body, debugBodyLimit, func(body []byte, truncated bool) {
			cfg.Debugger.RecordUpstreamBody(ctx, body, truncated)
		})
	}
	return resp, nil
}

func setUpstreamHeaders(req *http.Request, cfg Config) {
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	if cfg.Provider == providerOpenRouter {
		if cfg.HTTPReferer != "" {
			req.Header.Set("HTTP-Referer", cfg.HTTPReferer)
		}
		if cfg.AppTitle != "" {
			req.Header.Set("X-Title", cfg.AppTitle)
		}
	}
}

func effectiveModel(cfg Config, model string) string {
	if model != "" {
		return model
	}
	return cfg.Model
}

func responseModel(upstreamModel, fallback string) string {
	if upstreamModel != "" {
		return upstreamModel
	}
	return fallback
}

type streamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *ChatUsage     `json:"usage,omitempty"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Role             string     `json:"role,omitempty"`
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

func iterateSSE(body io.Reader, fn func(chunk streamChunk) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if err := streamError(data); err != nil {
			return err
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if err := fn(chunk); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func streamError(data string) error {
	var payload struct {
		Type  string          `json:"type"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil || len(payload.Error) == 0 || string(payload.Error) == "null" {
		return nil
	}

	var detail struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload.Error, &detail); err == nil && detail.Message != "" {
		if detail.Type != "" {
			return fmt.Errorf("%s: %s", detail.Type, detail.Message)
		}
		return fmt.Errorf("%s", detail.Message)
	}
	return fmt.Errorf("upstream stream error: %s", payload.Error)
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	payload, _ := json.Marshal(data)
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "proxy_error",
		},
	})
}
