package proxy

import "testing"

func boolPtr(value bool) *bool {
	return &value
}

func TestEnrichModelsUsesLiteLLMCapabilities(t *testing.T) {
	models := []ModelInfo{{ID: "gpt-5.4"}}
	catalog := map[string]liteLLMModelMetadata{
		"gpt-5.4": {
			MaxInputTokens:                  1_050_000,
			SupportsFunctionCalling:         boolPtr(true),
			SupportsParallelFunctionCalling: boolPtr(true),
			SupportsReasoning:               boolPtr(true),
			SupportsXHighReasoningEffort:    boolPtr(true),
			SupportsVision:                  boolPtr(true),
		},
	}

	if matched := EnrichModels(models, catalog, providerZen); matched != 1 {
		t.Fatalf("matched = %d, want 1", matched)
	}
	if models[0].ContextLength != 1_050_000 {
		t.Fatalf("context length = %d, want 1050000", models[0].ContextLength)
	}
	if models[0].Capabilities == nil || !models[0].Capabilities.SupportsParallelFunctionCalling {
		t.Fatalf("parallel function calling metadata was not applied: %#v", models[0].Capabilities)
	}
	if !models[0].Capabilities.SupportsXHighReasoningEffort || !models[0].Capabilities.SupportsVision {
		t.Fatalf("reasoning/vision metadata was not applied: %#v", models[0].Capabilities)
	}
}

func TestEnrichModelsPreservesProviderContextWindow(t *testing.T) {
	models := []ModelInfo{{ID: "provider/model:free", ContextLength: 64_000}}
	catalog := map[string]liteLLMModelMetadata{
		"openrouter/provider/model": {MaxInputTokens: 128_000},
	}

	EnrichModels(models, catalog, providerOpenRouter)
	if models[0].ContextLength != 64_000 {
		t.Fatalf("context length = %d, want provider value 64000", models[0].ContextLength)
	}
}

func TestCodexModelCatalogUsesConservativeCapabilities(t *testing.T) {
	models := []ModelInfo{
		{ID: "known", ContextLength: 200_000, Capabilities: &ModelCapabilities{
			SupportsFunctionCalling:         true,
			SupportsParallelFunctionCalling: true,
			SupportsReasoning:               true,
			SupportsXHighReasoningEffort:    true,
			SupportsVision:                  true,
		}},
		{ID: "unknown"},
	}

	catalog := codexModelCatalog(models, "instructions")
	known := catalog[0]
	if known["supports_parallel_tool_calls"] != true {
		t.Fatalf("known parallel tools = %v, want true", known["supports_parallel_tool_calls"])
	}
	if _, ok := known["apply_patch_tool_type"]; ok {
		t.Fatal("catalog must not advertise unsupported freeform apply_patch tools")
	}
	if modalities := known["input_modalities"].([]string); len(modalities) != 2 || modalities[1] != "image" {
		t.Fatalf("known modalities = %#v, want text and image", modalities)
	}
	levels := known["supported_reasoning_levels"].([]map[string]string)
	if levels[len(levels)-1]["effort"] != "xhigh" {
		t.Fatalf("known reasoning levels = %#v, want xhigh", levels)
	}

	unknown := catalog[1]
	if unknown["supports_parallel_tool_calls"] != false {
		t.Fatalf("unknown parallel tools = %v, want false", unknown["supports_parallel_tool_calls"])
	}
	if _, ok := unknown["apply_patch_tool_type"]; ok {
		t.Fatal("unknown model must not advertise apply_patch support")
	}
	if unknown["default_reasoning_level"] != "medium" {
		t.Fatalf("unknown model reasoning default = %v, want medium", unknown["default_reasoning_level"])
	}
	unknownLevels := unknown["supported_reasoning_levels"].([]map[string]string)
	if len(unknownLevels) != 3 || unknownLevels[0]["effort"] != "low" || unknownLevels[2]["effort"] != "high" {
		t.Fatalf("unknown reasoning levels = %#v, want low/medium/high", unknownLevels)
	}
}

func TestCodexModelCatalogHonorsExplicitNoReasoningMetadata(t *testing.T) {
	models := []ModelInfo{{
		ID: "non-reasoning",
		Capabilities: &ModelCapabilities{
			ReasoningSupportKnown: true,
			SupportsReasoning:     false,
		},
	}}

	entry := codexModelCatalog(models, "instructions")[0]
	if _, ok := entry["default_reasoning_level"]; ok {
		t.Fatal("explicitly non-reasoning model must not advertise a default")
	}
	if levels := entry["supported_reasoning_levels"].([]any); len(levels) != 0 {
		t.Fatalf("non-reasoning levels = %#v, want none", levels)
	}
}
