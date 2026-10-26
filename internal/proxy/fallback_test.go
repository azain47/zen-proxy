package proxy

import "testing"

func TestResolveFallbackModelUsesCurrentZenModelWhenDefaultDisappears(t *testing.T) {
	cfg := Config{Provider: providerZen, Model: "deepseek-v4.1-flash-free"}
	models := []ModelInfo{{ID: "current-zen-model"}}

	if got := resolveFallbackModel(cfg, models); got != "current-zen-model" {
		t.Fatalf("fallback = %q, want current upstream model", got)
	}
}

func TestResolveFallbackModelPrefersFreeOpenRouterModel(t *testing.T) {
	cfg := Config{Provider: providerOpenRouter, Model: "openrouter/free"}
	models := []ModelInfo{
		{ID: "paid/model", Pricing: &ModelPricing{Prompt: "0.001", Completion: "0.001"}},
		{ID: "current/free", Pricing: &ModelPricing{Prompt: "0", Completion: "0"}},
	}

	if got := resolveFallbackModel(cfg, models); got != "current/free" {
		t.Fatalf("fallback = %q, want current free model", got)
	}
}

func TestResolveFallbackModelPreservesExplicitModel(t *testing.T) {
	cfg := Config{Provider: providerOpenRouter, Model: "custom/private-model", ModelConfigured: true}
	models := []ModelInfo{{ID: "current/free", Pricing: &ModelPricing{Prompt: "0", Completion: "0"}}}

	if got := resolveFallbackModel(cfg, models); got != cfg.Model {
		t.Fatalf("fallback = %q, want explicit model %q", got, cfg.Model)
	}
}

func TestResolveFallbackModelDoesNotSelectPaidOpenRouterModel(t *testing.T) {
	cfg := Config{Provider: providerOpenRouter, Model: "openrouter/free"}
	models := []ModelInfo{{ID: "paid/model", Pricing: &ModelPricing{Prompt: "0.001", Completion: "0.001"}}}

	if got := resolveFallbackModel(cfg, models); got != cfg.Model {
		t.Fatalf("fallback = %q, want original safe default when no free model exists", got)
	}
}
