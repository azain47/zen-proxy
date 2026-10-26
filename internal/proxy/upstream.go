package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var proxyHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		DialTLSContext:        directTLSDialer,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

// directTLSDialer performs upstream TLS handshakes with OpenCode's Bun
// fingerprint so free-tier requests are treated as coming from the real
// client. Connections routed through an HTTP proxy fall back to the default
// TLS stack because DialTLSContext is only used for direct connections.
func directTLSDialer(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialOpenCodeTLS(ctx, network, addr, "")
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

// upstreamRequest translates a chat request for the configured upstream. When
// the free-tier client mode is active (Zen keyless), the request is reshaped
// to satisfy the OpenCode client gate: stream is forced on, a tool named
// exactly "read" is ensured, and OpenCode identity headers are attached. If
// the caller asked for a non-streaming reply, the forced SSE stream is
// aggregated back into a JSON completion before returning. The returned map
// renames tool names back to the client's original names (upstream name ->
// client name); it is nil when no renames happened.
func upstreamRequest(ctx context.Context, cfg Config, model string, messages []ChatMessage, stream bool, tools []ChatTool, opts chatRequestOptions) (*http.Response, map[string]string, error) {
	upstreamStream := stream
	var renames map[string]string
	if cfg.FreeTierClient {
		upstreamStream = true
		renames = map[string]string{}
		tools = normalizeReadTool(tools, renames)
		messages = ensureSystemEnv(messages)
		if len(renames) == 0 {
			renames = nil
		}
	}
	if cfg.Provider == providerZen && len(tools) > 0 {
		tools = sanitizeToolSchemasForZen(tools)
	}

	body := map[string]any{
		"model":    effectiveModel(cfg, model),
		"messages": messages,
		"stream":   upstreamStream,
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

	payload := marshalNoEscape(body)
	req, err := http.NewRequestWithContext(ctx, "POST", cfg.Upstream, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	setUpstreamHeaders(req, cfg)

	resp, err := executeUpstream(ctx, cfg, req, payload)
	if err != nil {
		return nil, nil, err
	}

	if cfg.FreeTierClient && !stream && resp.StatusCode == http.StatusOK {
		completion, aggErr := aggregateSSECompletion(resp.Body, renames)
		resp.Body.Close()
		if aggErr != nil {
			return nil, nil, fmt.Errorf("aggregating upstream stream: %w", aggErr)
		}
		return jsonResponse(http.StatusOK, completion), renames, nil
	}
	return resp, renames, nil
}

func executeUpstream(ctx context.Context, cfg Config, req *http.Request, payload []byte) (*http.Response, error) {
	if cfg.Debugger != nil {
		cfg.Debugger.RecordUpstreamRequest(ctx, req, payload)
	}

	resp, err := upstreamHTTPClient(cfg).Do(req)
	if err != nil {
		if cfg.Debugger != nil {
			cfg.Debugger.RecordUpstreamError(ctx, err)
		}
		return nil, err
	}
	if cfg.Debugger != nil {
		cfg.Debugger.RecordUpstreamResponse(ctx, resp)
		resp.Body = newCaptureReadCloser(resp.Body, 0, func(body []byte, truncated bool) {
			cfg.Debugger.RecordUpstreamBody(ctx, body, truncated)
		})
	}
	return resp, nil
}

func setUpstreamHeaders(req *http.Request, cfg Config) {
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	if cfg.FreeTierClient {
		version := cfg.OpenCodeVersion
		if version == "" {
			version = "1.19.4"
		}
		// Full User-Agent as sent by opencode 1.19.4 (runtime suffix included).
		req.Header.Set("User-Agent", "opencode/"+version+" ai-sdk/provider-utils/4.0.23 runtime/bun/1.4.4")
		sessionID := newOpenCodeSessionID()
		req.Header.Set("x-opencode-session", sessionID)
		req.Header.Set("x-opencode-session-id", sessionID)
		req.Header.Set("x-opencode-client", "cli")
		req.Header.Set("x-opencode-project", "global")
		if req.Method == http.MethodPost {
			req.Header.Set("x-opencode-request", newOpenCodeRequestID())
		}
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

// base62Alphabet matches the character set OpenCode uses for the random tail
// of session and message IDs (digits, uppercase, lowercase).
const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// OpenCode encodes a 48-bit timestamp inside the 12 hex-digit prefix of its
// session/message IDs. Session IDs count backwards from a fixed base so newer
// sessions sort first; message IDs count up from the unix epoch. The low 12
// bits tag the ID kind.
const (
	openCodeSessionIDBase = int64(0x1b000000000)
	openCodeSessionKind   = int64(0xffe)
	openCodeMessageKind   = int64(0x001)
)

func openCodeIDHead(head int64) string {
	return fmt.Sprintf("%012x", head&0xffffffffffff)
}

func randomBase62Tail() string {
	randBytes := make([]byte, 14)
	if _, err := rand.Read(randBytes); err != nil {
		// crypto/rand failing is unrecoverable for an ID; fall back to a
		// time-derived value so the shape stays valid.
		seed := time.Now().UnixNano()
		for i := range randBytes {
			randBytes[i] = byte(seed >> (uint(i%8) * 8))
		}
	}
	tail := make([]byte, 14)
	for i := range tail {
		tail[i] = base62Alphabet[int(randBytes[i])%62]
	}
	return string(tail)
}

// newOpenCodeSessionID produces a session ID in the format OpenCode itself
// generates: "ses_" + 12 lowercase hex chars encoding an inverted unix-ms
// timestamp tagged with 0xffe, followed by 14 random base62 chars. The
// free-tier gate validates this shape.
func newOpenCodeSessionID() string {
	head := (openCodeSessionIDBase-time.Now().UnixMilli())<<12 | openCodeSessionKind
	return "ses_" + openCodeIDHead(head) + randomBase62Tail()
}

// newOpenCodeRequestID produces the per-request "msg_" identifier OpenCode
// sends in x-opencode-request: a forward-counting unix-ms timestamp tagged
// with 0x001, plus a random base62 tail.
func newOpenCodeRequestID() string {
	head := time.Now().UnixMilli()<<12 | openCodeMessageKind
	return "msg_" + openCodeIDHead(head) + randomBase62Tail()
}

// openCodeEnvBlock builds the <env> section OpenCode appends to its system
// prompt. The free-tier gate rejects requests whose system message lacks it.
func openCodeEnvBlock() string {
	workingDir, err := os.Getwd()
	if err != nil || workingDir == "" {
		workingDir = "/"
	}
	gitRepo := "no"
	if info, err := os.Stat(filepath.Join(workingDir, ".git")); err == nil && info.IsDir() {
		gitRepo = "yes"
	}
	return fmt.Sprintf("<env>\n  Working directory: %s\n  Workspace root folder: %s\n  Is directory a git repo: %s\n  Platform: %s\n  Today's date: %s\n</env>",
		workingDir, workingDir, gitRepo, runtime.GOOS, time.Now().Format("Mon Jan 2 2006"))
}

// ensureSystemEnv guarantees the translated message list starts with a system
// message containing the <env> block, appending to an existing system prompt
// when one is present.
func ensureSystemEnv(messages []ChatMessage) []ChatMessage {
	env := openCodeEnvBlock()
	if len(messages) > 0 && messages[0].Role == "system" {
		var text string
		if json.Unmarshal(messages[0].Content, &text) == nil {
			messages[0].Content = jsonStr(text + "\n\n" + env)
			return messages
		}
		var parts []map[string]any
		if json.Unmarshal(messages[0].Content, &parts) == nil {
			parts = append(parts, map[string]any{"type": "text", "text": env})
			encoded, err := json.Marshal(parts)
			if err == nil {
				messages[0].Content = encoded
				return messages
			}
		}
	}
	return append([]ChatMessage{{Role: "system", Content: jsonStr(env)}}, messages...)
}

// normalizeReadTool ensures the tool list contains a function named exactly
// "read", which the Zen free-tier gate requires. If the client already has a
// read-like tool ("read", "Read", "read_file", ...), it is renamed to "read"
// and the original name is recorded in renames so responses can be mapped
// back. Otherwise a minimal read tool is injected (with no rename entry, so
// any call to it is passed through as-is).
func normalizeReadTool(tools []ChatTool, renames map[string]string) []ChatTool {
	readLike := func(name string) bool {
		lower := strings.ToLower(name)
		return lower == "read" || lower == "read_file" || lower == "readfile" || lower == "read_text" || lower == "cat"
	}
	for i := range tools {
		name := tools[i].Function.Name
		if name == "read" {
			return tools
		}
		if readLike(name) {
			renames["read"] = name
			tools[i].Function.Name = "read"
			return tools
		}
	}
	return append(tools, ChatTool{
		Type: "function",
		Function: ChatFunction{
			Name:        "read",
			Description: "Read the contents of a file from the local filesystem.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the file"},"offset":{"type":"integer","description":"Line offset to start from"},"limit":{"type":"integer","description":"Maximum number of lines to read"}},"required":["path"]}`),
		},
	})
}

// Zen's Console backend rejects some JSON Schema keywords inside tool
// definitions with an opaque 400 that echoes only the model name. Live
// bisection of Claude Code's Artifact tool showed a regex pattern alone is
// enough to trigger it; pattern is stripped along with the keywords that were
// already being dropped. Tool schemas are normalized for Zen by dropping
// these keywords at every schema position while leaving property names and
// everything else intact.
var zenUnsupportedSchemaKeys = []string{
	"$schema",
	"minimum",
	"maximum",
	"exclusiveMinimum",
	"exclusiveMaximum",
	"propertyNames",
	"pattern",
}

var schemaMapKeywords = []string{"properties", "patternProperties", "definitions", "$defs", "dependentSchemas"}
var schemaListKeywords = []string{"anyOf", "oneOf", "allOf", "prefixItems"}
var schemaObjectKeywords = []string{"items", "additionalItems", "additionalProperties", "contains", "not", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties"}

func stripUnsupportedSchemaKeys(schema map[string]any) {
	for _, key := range zenUnsupportedSchemaKeys {
		delete(schema, key)
	}
	for _, keyword := range schemaMapKeywords {
		subs, ok := schema[keyword].(map[string]any)
		if !ok {
			continue
		}
		for _, sub := range subs {
			if m, ok := sub.(map[string]any); ok {
				stripUnsupportedSchemaKeys(m)
			}
		}
	}
	for _, keyword := range schemaListKeywords {
		entries, ok := schema[keyword].([]any)
		if !ok {
			continue
		}
		for _, entry := range entries {
			if m, ok := entry.(map[string]any); ok {
				stripUnsupportedSchemaKeys(m)
			}
		}
	}
	for _, keyword := range schemaObjectKeywords {
		if m, ok := schema[keyword].(map[string]any); ok {
			stripUnsupportedSchemaKeys(m)
		}
	}
}

func sanitizeToolSchemaForZen(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var schema map[string]any
	if err := decoder.Decode(&schema); err != nil {
		return raw
	}
	stripUnsupportedSchemaKeys(schema)
	sanitized, err := json.Marshal(schema)
	if err != nil {
		return raw
	}
	return sanitized
}

func sanitizeToolSchemasForZen(tools []ChatTool) []ChatTool {
	out := make([]ChatTool, len(tools))
	copy(out, tools)
	for i := range out {
		out[i].Function.Parameters = sanitizeToolSchemaForZen(out[i].Function.Parameters)
	}
	return out
}

// aggregateSSECompletion consumes a chat-completions SSE stream and folds it
// into a single ChatCompletion, so non-streaming clients keep working when
// the free-tier gate forces streaming upstream.
func aggregateSSECompletion(body io.Reader, renames map[string]string) (*ChatCompletion, error) {
	var completion ChatCompletion
	var content, reasoning strings.Builder
	toolCalls := map[int]*ToolCall{}
	var toolOrder []int
	finishReason := ""

	err := iterateSSE(body, func(chunk streamChunk) error {
		if chunk.ID != "" {
			completion.ID = chunk.ID
		}
		if chunk.Model != "" {
			completion.Model = chunk.Model
		}
		if chunk.Usage != nil {
			completion.Usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		delta := chunk.Choices[0].Delta
		content.WriteString(delta.Content)
		reasoning.WriteString(delta.ReasoningContent)
		for _, tc := range delta.ToolCalls {
			existing, ok := toolCalls[tc.Index]
			if !ok {
				call := ToolCall{ID: tc.ID, Type: "function"}
				toolCalls[tc.Index] = &call
				toolOrder = append(toolOrder, tc.Index)
				existing = &call
			}
			if tc.ID != "" {
				existing.ID = tc.ID
			}
			if tc.Function.Name != "" {
				existing.Function.Name = restoreToolName(renames, tc.Function.Name)
			}
			existing.Function.Arguments += tc.Function.Arguments
		}
		if chunk.Choices[0].FinishReason != nil {
			finishReason = *chunk.Choices[0].FinishReason
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if completion.ID == "" {
		completion.ID = "cmpl_proxy"
	}
	if completion.Object == "" {
		completion.Object = "chat.completion"
	}

	message := CompletionMessage{
		Role:             "assistant",
		Content:          content.String(),
		ReasoningContent: reasoning.String(),
	}
	for _, index := range toolOrder {
		message.ToolCalls = append(message.ToolCalls, *toolCalls[index])
	}
	if finishReason == "" {
		if len(message.ToolCalls) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}
	completion.Choices = []CompletionChoice{{
		Index:        0,
		Message:      message,
		FinishReason: finishReason,
	}}
	return &completion, nil
}

// restoreToolName maps an upstream tool name back to the client's original
// name when normalizeReadTool renamed it.
func restoreToolName(renames map[string]string, name string) string {
	if original, ok := renames[name]; ok {
		return original
	}
	return name
}

// restoreToolCallNames applies restoreToolName across a tool-call slice.
func restoreToolCallNames(calls []ToolCall, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	for i := range calls {
		calls[i].Function.Name = restoreToolName(renames, calls[i].Function.Name)
	}
}

// jsonResponse wraps a value in a synthetic *http.Response so aggregated
// completions flow through the same handler code as a real upstream reply.
func jsonResponse(status int, value any) *http.Response {
	payload, err := json.Marshal(value)
	if err != nil {
		payload = []byte(`{"error":{"message":"failed to encode aggregated response","type":"proxy_error"}}`)
		status = http.StatusBadGateway
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(payload)),
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

func marshalNoEscape(value any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	payload := marshalNoEscape(data)
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
