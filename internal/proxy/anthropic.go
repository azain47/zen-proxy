package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func handleAnthropic(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid request body")
			return
		}

		model := effectiveModel(cfg, req.Model)
		if cfg.Debugger != nil {
			cfg.Debugger.SetRequest(r.Context(), "anthropic-messages", model)
		}
		messages := translateAnthropicMessages(req)
		tools := translateAnthropicTools(req.Tools)
		stream := req.Stream
		opts := anthropicChatRequestOptions(req)

		resp, err := upstreamRequest(r.Context(), cfg, model, messages, stream, tools, opts)
		if err != nil {
			writeError(w, 502, "upstream error: "+err.Error())
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			writeError(w, resp.StatusCode, "upstream: "+string(body))
			return
		}

		if !stream {
			anthropicNonStreaming(w, resp.Body, model)
			return
		}
		anthropicStreaming(w, resp.Body, model)
	}
}

func handleTokenCount(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid request body")
			return
		}
		if cfg.Debugger != nil {
			cfg.Debugger.SetRequest(r.Context(), "anthropic-token-count", effectiveModel(cfg, req.Model))
		}
		messages := translateAnthropicMessages(req)
		estimate := 0
		for _, m := range messages {
			estimate += len(string(m.Content)) / 4
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"input_tokens": max(estimate, 1)})
	}
}

func anthropicNonStreaming(w http.ResponseWriter, body io.Reader, model string) {
	var completion ChatCompletion
	if err := json.NewDecoder(body).Decode(&completion); err != nil {
		writeError(w, 502, "failed to decode upstream response")
		return
	}
	if len(completion.Choices) == 0 {
		writeError(w, 502, "upstream response missing choices")
		return
	}

	msg := completion.Choices[0].Message
	content := buildAnthropicContent(msg)
	stopReason := mapFinishReason(completion.Choices[0].FinishReason)

	resp := anthropicResponse{
		ID:         "msg_" + completion.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      responseModel(completion.Model, model),
		Content:    content,
		StopReason: stopReason,
		Usage:      anthropicUsageFromChat(completion.Usage),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func anthropicStreaming(w http.ResponseWriter, body io.Reader, model string) {
	sseHeaders(w)

	msgID := "msg_proxy"
	writeSSE(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":          msgID,
			"type":        "message",
			"role":        "assistant",
			"content":     []any{},
			"model":       model,
			"stop_reason": nil,
			"usage":       map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})

	nextBlockIndex := 0
	inThinking := false
	thinkingIndex := -1
	inText := false
	textIndex := -1
	emittedBlock := false
	sawToolUse := false
	stopReason := "end_turn"
	outputTokens := 0
	gotChunk := false
	toolBlocks := map[int]*anthropicStreamToolBlock{}
	var toolOrder []int

	closeThinking := func() {
		if inThinking {
			writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": thinkingIndex})
			inThinking = false
		}
	}
	closeText := func() {
		if inText {
			writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": textIndex})
			inText = false
		}
	}
	closeTools := func() {
		for _, openAIIndex := range toolOrder {
			tb := toolBlocks[openAIIndex]
			if tb != nil && tb.Open {
				writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": tb.BlockIndex})
				tb.Open = false
			}
		}
	}

	err := iterateSSE(body, func(chunk streamChunk) error {
		gotChunk = true
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil {
				outputTokens = chunk.Usage.CompletionTokens
			}
			return nil
		}
		delta := chunk.Choices[0].Delta
		finish := chunk.Choices[0].FinishReason
		if chunk.Usage != nil {
			outputTokens = chunk.Usage.CompletionTokens
		}

		if delta.ReasoningContent != "" {
			closeText()
			closeTools()
			if !inThinking {
				thinkingIndex = nextBlockIndex
				nextBlockIndex++
				writeSSE(w, "content_block_start", map[string]any{
					"type":          "content_block_start",
					"index":         thinkingIndex,
					"content_block": map[string]string{"type": "thinking", "thinking": ""},
				})
				inThinking = true
				emittedBlock = true
			}
			writeSSE(w, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": thinkingIndex,
				"delta": map[string]string{"type": "thinking_delta", "thinking": delta.ReasoningContent},
			})
		}

		if delta.Content != "" {
			closeThinking()
			closeTools()
			if !inText {
				textIndex = nextBlockIndex
				nextBlockIndex++
				writeSSE(w, "content_block_start", map[string]any{
					"type":          "content_block_start",
					"index":         textIndex,
					"content_block": map[string]string{"type": "text", "text": ""},
				})
				inText = true
				emittedBlock = true
			}
			writeSSE(w, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": textIndex,
				"delta": map[string]string{"type": "text_delta", "text": delta.Content},
			})
		}

		if len(delta.ToolCalls) > 0 {
			closeText()
			closeThinking()
			for _, tc := range delta.ToolCalls {
				tb := toolBlocks[tc.Index]
				if tb == nil {
					id := tc.ID
					if id == "" {
						id = fmt.Sprintf("toolu_proxy_%d", tc.Index)
					}
					name := tc.Function.Name
					tb = &anthropicStreamToolBlock{
						BlockIndex: nextBlockIndex,
						ID:         id,
						Name:       name,
						Open:       true,
					}
					nextBlockIndex++
					toolBlocks[tc.Index] = tb
					toolOrder = append(toolOrder, tc.Index)
					sawToolUse = true
					emittedBlock = true
					writeSSE(w, "content_block_start", map[string]any{
						"type":  "content_block_start",
						"index": tb.BlockIndex,
						"content_block": map[string]any{
							"type":  "tool_use",
							"id":    tb.ID,
							"name":  tb.Name,
							"input": map[string]any{},
						},
					})
				} else if tc.Function.Name != "" {
					tb.Name += tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					writeSSE(w, "content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": tb.BlockIndex,
						"delta": map[string]string{"type": "input_json_delta", "partial_json": tc.Function.Arguments},
					})
				}
			}
		}

		if finish != nil {
			stopReason = mapFinishReason(*finish)
			if sawToolUse {
				stopReason = "tool_use"
			}
		}

		return nil
	})

	closeThinking()
	closeText()
	closeTools()

	if err != nil {
		writeAnthropicStreamError(w, err)
		return
	}
	if !gotChunk {
		writeAnthropicStreamError(w, fmt.Errorf("upstream stream ended without data"))
		return
	}
	if !emittedBlock {
		index := nextBlockIndex
		writeSSE(w, "content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         index,
			"content_block": map[string]string{"type": "text", "text": ""},
		})
		writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
	}

	writeSSE(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]int{"output_tokens": outputTokens},
	})
	writeSSE(w, "message_stop", map[string]string{"type": "message_stop"})
}

type anthropicStreamToolBlock struct {
	BlockIndex int
	ID         string
	Name       string
	Open       bool
}

func writeAnthropicStreamError(w http.ResponseWriter, err error) {
	writeSSE(w, "error", map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "api_error",
			"message": err.Error(),
		},
	})
}

func translateAnthropicMessages(req anthropicRequest) []ChatMessage {
	var messages []ChatMessage

	if req.System != nil {
		sysContent := extractSystemContent(req.System)
		if sysContent != "" {
			messages = append(messages, ChatMessage{
				Role:    "system",
				Content: jsonStr(sysContent),
			})
		}
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "user", "assistant":
			converted := convertAnthropicMessage(m)
			messages = append(messages, converted...)
		}
	}
	return messages
}

func convertAnthropicMessage(m anthropicMessage) []ChatMessage {
	if m.ContentRaw == nil {
		return nil
	}

	var strContent string
	if err := json.Unmarshal(m.ContentRaw, &strContent); err == nil {
		return []ChatMessage{{Role: m.Role, Content: jsonStr(strContent)}}
	}

	var blocks []anthropicContentBlock
	if err := json.Unmarshal(m.ContentRaw, &blocks); err != nil {
		return nil
	}

	if m.Role == "user" {
		return convertUserBlocks(blocks)
	}
	return convertAssistantBlocks(blocks)
}

func convertUserBlocks(blocks []anthropicContentBlock) []ChatMessage {
	var toolResults []ChatMessage
	var contentParts []map[string]any

	for _, b := range blocks {
		switch b.Type {
		case "text":
			contentParts = append(contentParts, map[string]any{"type": "text", "text": b.Text})
		case "image":
			if b.Source != nil {
				url := fmt.Sprintf("data:%s;base64,%s", b.Source.MediaType, b.Source.Data)
				contentParts = append(contentParts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]string{"url": url},
				})
			}
		case "tool_result":
			content := b.Text
			if content == "" {
				if b.ContentRaw != nil {
					var resultBlocks []anthropicContentBlock
					if json.Unmarshal(b.ContentRaw, &resultBlocks) == nil {
						var parts []string
						for _, rb := range resultBlocks {
							if rb.Type == "text" {
								parts = append(parts, rb.Text)
							}
						}
						content = strings.Join(parts, "\n")
					} else {
						content = string(b.ContentRaw)
					}
				}
			}
			toolResults = append(toolResults, ChatMessage{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    jsonStr(content),
			})
		}
	}

	var result []ChatMessage
	if len(contentParts) > 0 {
		raw, _ := json.Marshal(contentParts)
		result = append(result, ChatMessage{Role: "user", Content: raw})
	}
	result = append(result, toolResults...)
	return result
}

func convertAssistantBlocks(blocks []anthropicContentBlock) []ChatMessage {
	var textParts []string
	var toolCalls []ToolCall

	for _, b := range blocks {
		switch b.Type {
		case "text":
			textParts = append(textParts, b.Text)
		case "thinking":
			// Skip thinking blocks in conversation history
		case "tool_use":
			args, _ := json.Marshal(b.Input)
			toolCalls = append(toolCalls, ToolCall{
				ID:   b.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      b.Name,
					Arguments: string(args),
				},
			})
		}
	}

	msg := ChatMessage{
		Role:    "assistant",
		Content: jsonStr(strings.Join(textParts, "")),
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}
	return []ChatMessage{msg}
}

func translateAnthropicTools(tools []anthropicTool) []ChatTool {
	var result []ChatTool
	for _, t := range tools {
		result = append(result, ChatTool{
			Type: "function",
			Function: ChatFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return result
}

func buildAnthropicContent(msg CompletionMessage) []map[string]any {
	var content []map[string]any
	if msg.ReasoningContent != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": msg.ReasoningContent})
	}
	if msg.Content != "" {
		content = append(content, map[string]any{"type": "text", "text": msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		var input any
		json.Unmarshal([]byte(tc.Function.Arguments), &input)
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Function.Name,
			"input": input,
		})
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	return content
}

func anthropicReasoningEffort(t *anthropicThinking) string {
	if t == nil || t.Type != "enabled" {
		return ""
	}
	switch {
	case t.BudgetTokens <= 0:
		return ""
	case t.BudgetTokens < 4000:
		return "low"
	case t.BudgetTokens < 16000:
		return "medium"
	default:
		return "high"
	}
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func extractSystemContent(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []anthropicContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func jsonStr(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func anthropicUsageFromChat(usage ChatUsage) anthropicUsage {
	cacheRead := usage.PromptCacheHitTokens
	if cacheRead == 0 && usage.PromptTokensDetails != nil {
		cacheRead = usage.PromptTokensDetails.CachedTokens
	}
	return anthropicUsage{
		InputTokens:           usage.PromptTokens,
		OutputTokens:          usage.CompletionTokens,
		CacheReadInputTokens:  cacheRead,
		PromptCacheHitTokens:  usage.PromptCacheHitTokens,
		PromptCacheMissTokens: usage.PromptCacheMissTokens,
	}
}

type anthropicRequest struct {
	Model         string               `json:"model"`
	System        json.RawMessage      `json:"system,omitempty"`
	Messages      []anthropicMessage   `json:"messages"`
	MaxTokens     int                  `json:"max_tokens"`
	Stream        bool                 `json:"stream"`
	Tools         []anthropicTool      `json:"tools,omitempty"`
	Thinking      *anthropicThinking   `json:"thinking,omitempty"`
	Temperature   *float64             `json:"temperature,omitempty"`
	TopP          *float64             `json:"top_p,omitempty"`
	StopSequences []string             `json:"stop_sequences,omitempty"`
	ToolChoice    *anthropicToolChoice `json:"tool_choice,omitempty"`
}

type anthropicToolChoice struct {
	Type               string `json:"type"`
	Name               string `json:"name,omitempty"`
	DisableParallelUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

func anthropicChatRequestOptions(req anthropicRequest) chatRequestOptions {
	opts := chatRequestOptions{
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		Stop:            req.StopSequences,
		ReasoningEffort: anthropicReasoningEffort(req.Thinking),
	}
	if req.ToolChoice == nil {
		return opts
	}
	parallel := !req.ToolChoice.DisableParallelUse
	opts.ParallelToolCalls = &parallel
	switch req.ToolChoice.Type {
	case "auto":
		opts.ToolChoice = json.RawMessage(`"auto"`)
	case "any":
		opts.ToolChoice = json.RawMessage(`"required"`)
	case "tool":
		choice, _ := json.Marshal(map[string]any{
			"type":     "function",
			"function": map[string]string{"name": req.ToolChoice.Name},
		})
		opts.ToolChoice = choice
	}
	return opts
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type anthropicMessage struct {
	Role       string          `json:"role"`
	ContentRaw json.RawMessage `json:"content"`
}

type anthropicContentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Input      any             `json:"input,omitempty"`
	ToolUseID  string          `json:"tool_use_id,omitempty"`
	Source     *imageSource    `json:"source,omitempty"`
	ContentRaw json.RawMessage `json:"content,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicResponse struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Role       string           `json:"role"`
	Model      string           `json:"model"`
	Content    []map[string]any `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      anthropicUsage   `json:"usage"`
}

type anthropicUsage struct {
	InputTokens           int `json:"input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	CacheReadInputTokens  int `json:"cache_read_input_tokens,omitempty"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
}
