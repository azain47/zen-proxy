package proxy

import (
	"encoding/json"
	"testing"
)

func TestAnthropicOutputConfigEffortTranslates(t *testing.T) {
	var req anthropicRequest
	if err := json.Unmarshal([]byte(`{"model":"x-preview-f-free","max_tokens":32,"messages":[],"output_config":{"effort":"high"}}`), &req); err != nil {
		t.Fatalf("unmarshal Anthropic request: %v", err)
	}

	opts := anthropicChatRequestOptions(req)
	if opts.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want high", opts.ReasoningEffort)
	}
}

func TestAnthropicOutputConfigEffortFallsBackToThinkingBudget(t *testing.T) {
	req := anthropicRequest{
		Thinking:     &anthropicThinking{Type: "enabled", BudgetTokens: 2000},
		OutputConfig: &anthropicOutputConfig{Effort: "unsupported"},
	}

	opts := anthropicChatRequestOptions(req)
	if opts.ReasoningEffort != "low" {
		t.Fatalf("ReasoningEffort = %q, want low fallback", opts.ReasoningEffort)
	}
}
