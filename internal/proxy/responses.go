package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func handleResponses(cfg Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid request body")
			return
		}

		model := effectiveModel(cfg, req.Model)
		messages := translateResponsesInput(req)
		tools := translateResponsesTools(req.Tools)
		stream := req.Stream

		resp, err := upstreamRequest(cfg, model, messages, stream, tools)
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
			responsesNonStreaming(w, resp.Body, model)
			return
		}
		responsesStreaming(w, resp.Body, model)
	}
}

func responsesNonStreaming(w http.ResponseWriter, body io.Reader, model string) {
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
	respID := "resp_" + completion.ID
	msgID := "msg_" + completion.ID

	var output []any

	if msg.Content != "" {
		output = append(output, map[string]any{
			"id":     msgID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{
				{"type": "output_text", "text": msg.Content},
			},
		})
	}

	for _, tc := range msg.ToolCalls {
		output = append(output, map[string]any{
			"id":        "fc_" + tc.ID,
			"type":      "function_call",
			"status":    "completed",
			"name":      tc.Function.Name,
			"arguments": tc.Function.Arguments,
			"call_id":   tc.ID,
		})
	}

	resp := map[string]any{
		"id":     respID,
		"object": "response",
		"status": "completed",
		"model":  responseModel(completion.Model, model),
		"output": output,
		"usage":  responsesUsageFromChat(completion.Usage),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func responsesStreaming(w http.ResponseWriter, body io.Reader, model string) {
	sseHeaders(w)

	respID := "resp_proxy"
	nextOutputIndex := 0
	nextMessageIndex := 0
	contentIndex := 0
	gotChunk := false
	var outputText strings.Builder
	var output []any
	var currentText *responsesStreamTextItem
	toolItems := map[int]*responsesStreamToolItem{}
	var toolOrder []int

	writeSSE(w, "response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id": respID, "object": "response", "status": "in_progress",
			"model": model, "output": []any{},
		},
	})

	finishText := func() {
		if currentText == nil || currentText.Done {
			return
		}
		text := currentText.Text.String()
		finishTextBlock(w, currentText.ID, currentText.OutputIndex, currentText.ContentIndex, text)
		item := map[string]any{
			"id":     currentText.ID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{
				{"type": "output_text", "text": text, "annotations": []any{}},
			},
		}
		writeSSE(w, "response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": currentText.OutputIndex,
			"item":         item,
		})
		output = append(output, item)
		currentText.Done = true
		currentText = nil
	}

	finishTool := func(tool *responsesStreamToolItem) {
		if tool == nil || tool.Done {
			return
		}
		args := tool.Args.String()
		writeSSE(w, "response.function_call_arguments.done", map[string]any{
			"type":         "response.function_call_arguments.done",
			"item_id":      tool.ID,
			"output_index": tool.OutputIndex,
			"arguments":    args,
		})
		item := map[string]any{
			"id":        tool.ID,
			"type":      "function_call",
			"status":    "completed",
			"name":      tool.Name,
			"arguments": args,
			"call_id":   tool.CallID,
		}
		writeSSE(w, "response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": tool.OutputIndex,
			"item":         item,
		})
		output = append(output, item)
		tool.Done = true
	}

	finishTools := func() {
		for _, openAIIndex := range toolOrder {
			finishTool(toolItems[openAIIndex])
		}
	}

	startText := func() *responsesStreamTextItem {
		finishTools()
		if currentText != nil {
			return currentText
		}
		msgID := "msg_proxy"
		if nextMessageIndex > 0 {
			msgID = "msg_proxy_" + strconv.Itoa(nextMessageIndex)
		}
		nextMessageIndex++
		currentText = &responsesStreamTextItem{
			ID:           msgID,
			OutputIndex:  nextOutputIndex,
			ContentIndex: contentIndex,
		}
		nextOutputIndex++
		writeSSE(w, "response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": currentText.OutputIndex,
			"item": map[string]any{
				"id": msgID, "type": "message", "role": "assistant",
				"status": "in_progress", "content": []any{},
			},
		})
		writeSSE(w, "response.content_part.added", map[string]any{
			"type":          "response.content_part.added",
			"item_id":       msgID,
			"output_index":  currentText.OutputIndex,
			"content_index": currentText.ContentIndex,
			"part":          map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
		})
		return currentText
	}

	startTool := func(openAIIndex int, tc ToolCall) *responsesStreamToolItem {
		finishText()
		tool := toolItems[openAIIndex]
		if tool != nil {
			return tool
		}
		callID := tc.ID
		if callID == "" {
			callID = "call_proxy_" + strconv.Itoa(openAIIndex)
		}
		tool = &responsesStreamToolItem{
			ID:          "fc_" + callID,
			CallID:      callID,
			Name:        tc.Function.Name,
			OutputIndex: nextOutputIndex,
		}
		nextOutputIndex++
		toolItems[openAIIndex] = tool
		toolOrder = append(toolOrder, openAIIndex)
		writeSSE(w, "response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": tool.OutputIndex,
			"item": map[string]any{
				"id": tool.ID, "type": "function_call", "status": "in_progress",
				"name": tool.Name, "arguments": "", "call_id": tool.CallID,
			},
		})
		return tool
	}

	usage := responsesUsageFromChat(ChatUsage{})
	err := iterateSSE(body, func(chunk streamChunk) error {
		gotChunk = true
		if chunk.Usage != nil {
			usage = responsesUsageFromChat(*chunk.Usage)
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			text := startText()
			text.Text.WriteString(delta.Content)
			outputText.WriteString(delta.Content)
			writeSSE(w, "response.output_text.delta", map[string]any{
				"type":          "response.output_text.delta",
				"item_id":       text.ID,
				"output_index":  text.OutputIndex,
				"content_index": text.ContentIndex,
				"delta":         delta.Content,
			})
		}

		if len(delta.ToolCalls) > 0 {
			for _, tc := range delta.ToolCalls {
				tool := startTool(tc.Index, tc)
				if tc.Function.Name != "" && tool.Name != tc.Function.Name {
					tool.Name += tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					tool.Args.WriteString(tc.Function.Arguments)
					writeSSE(w, "response.function_call_arguments.delta", map[string]any{
						"type":         "response.function_call_arguments.delta",
						"item_id":      tool.ID,
						"output_index": tool.OutputIndex,
						"delta":        tc.Function.Arguments,
					})
				}
			}
		}

		return nil
	})

	if err != nil {
		writeResponsesStreamFailure(w, respID, model, err)
		return
	}
	if !gotChunk {
		writeResponsesStreamFailure(w, respID, model, fmt.Errorf("upstream stream ended without data"))
		return
	}

	finishText()
	finishTools()

	writeSSE(w, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":          respID,
			"object":      "response",
			"status":      "completed",
			"model":       model,
			"output":      output,
			"output_text": outputText.String(),
			"usage":       usage,
		},
	})
}

type responsesStreamTextItem struct {
	ID           string
	OutputIndex  int
	ContentIndex int
	Text         strings.Builder
	Done         bool
}

type responsesStreamToolItem struct {
	ID          string
	CallID      string
	Name        string
	OutputIndex int
	Args        strings.Builder
	Done        bool
}

func writeResponsesStreamFailure(w http.ResponseWriter, respID, model string, err error) {
	writeSSE(w, "response.failed", map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id":     respID,
			"object": "response",
			"status": "failed",
			"model":  model,
			"error": map[string]string{
				"type":    "server_error",
				"message": err.Error(),
			},
		},
	})
}

func responsesUsageFromChat(usage ChatUsage) map[string]any {
	cachedTokens := usage.PromptCacheHitTokens
	if cachedTokens == 0 && usage.PromptTokensDetails != nil {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
	}

	result := map[string]any{
		"input_tokens":  usage.PromptTokens,
		"output_tokens": usage.CompletionTokens,
		"total_tokens":  usage.TotalTokens,
		"input_tokens_details": map[string]int{
			"cached_tokens": cachedTokens,
		},
	}
	if usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.ReasoningTokens > 0 {
		result["output_tokens_details"] = map[string]int{
			"reasoning_tokens": usage.CompletionTokensDetails.ReasoningTokens,
		}
	}
	if usage.PromptCacheHitTokens > 0 {
		result["prompt_cache_hit_tokens"] = usage.PromptCacheHitTokens
	}
	if usage.PromptCacheMissTokens > 0 {
		result["prompt_cache_miss_tokens"] = usage.PromptCacheMissTokens
	}
	return result
}

func finishTextBlock(w http.ResponseWriter, msgID string, outputIndex, contentIndex int, text string) {
	writeSSE(w, "response.output_text.done", map[string]any{
		"type":          "response.output_text.done",
		"item_id":       msgID,
		"output_index":  outputIndex,
		"content_index": contentIndex,
		"text":          text,
	})
	writeSSE(w, "response.content_part.done", map[string]any{
		"type":          "response.content_part.done",
		"item_id":       msgID,
		"output_index":  outputIndex,
		"content_index": contentIndex,
		"part":          map[string]any{"type": "output_text", "text": text},
	})
}

func translateResponsesInput(req responsesRequest) []ChatMessage {
	var messages []ChatMessage

	if req.Instructions != "" {
		messages = append(messages, ChatMessage{
			Role:    "system",
			Content: jsonStr(req.Instructions),
		})
	}

	if req.Input == nil {
		return messages
	}

	var strInput string
	if json.Unmarshal(req.Input, &strInput) == nil {
		messages = append(messages, ChatMessage{Role: "user", Content: jsonStr(strInput)})
		return messages
	}

	var items []responsesInputItem
	if err := json.Unmarshal(req.Input, &items); err != nil {
		return messages
	}

	for i := 0; i < len(items); i++ {
		item := items[i]
		switch item.Type {
		case "message", "":
			role := item.Role
			if role == "" {
				role = "user"
			}
			content := extractResponsesContent(item)
			messages = append(messages, ChatMessage{Role: role, Content: jsonStr(content)})

		case "function_call":
			msg := ChatMessage{
				Role:    "assistant",
				Content: jsonStr(""),
				ToolCalls: []ToolCall{{
					ID:   item.CallID,
					Type: "function",
					Function: FunctionCall{
						Name:      item.Name,
						Arguments: item.Arguments,
					},
				}},
			}
			if len(messages) > 0 && messages[len(messages)-1].Role == "assistant" && len(messages[len(messages)-1].ToolCalls) > 0 {
				messages[len(messages)-1].ToolCalls = append(messages[len(messages)-1].ToolCalls, msg.ToolCalls[0])
			} else {
				messages = append(messages, msg)
			}

		case "function_call_output":
			messages = append(messages, ChatMessage{
				Role:       "tool",
				ToolCallID: item.CallID,
				Content:    jsonStr(item.Output),
			})
		}
	}
	return messages
}

func extractResponsesContent(item responsesInputItem) string {
	if item.Content == nil {
		return ""
	}
	var s string
	if json.Unmarshal(item.Content, &s) == nil {
		return s
	}
	var parts []responsesContentPart
	if json.Unmarshal(item.Content, &parts) == nil {
		var texts []string
		for _, p := range parts {
			if p.Type == "input_text" || p.Type == "text" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return string(item.Content)
}

func translateResponsesTools(tools []responsesTool) []ChatTool {
	var result []ChatTool
	for _, t := range tools {
		if t.Type == "function" {
			result = append(result, ChatTool{
				Type: "function",
				Function: ChatFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.Parameters,
				},
			})
		}
	}
	return result
}

type responsesRequest struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions,omitempty"`
	Input        json.RawMessage `json:"input"`
	Stream       bool            `json:"stream"`
	Tools        []responsesTool `json:"tools,omitempty"`
}

type responsesInputItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    string          `json:"output,omitempty"`
}

type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}
