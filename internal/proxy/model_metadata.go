package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ModelCapabilities struct {
	SupportsFunctionCalling         bool
	SupportsParallelFunctionCalling bool
	ReasoningSupportKnown           bool
	SupportsReasoning               bool
	SupportsMinimalReasoningEffort  bool
	SupportsXHighReasoningEffort    bool
	SupportsVision                  bool
}

type liteLLMModelMetadata struct {
	MaxInputTokens                  int   `json:"max_input_tokens"`
	SupportsFunctionCalling         *bool `json:"supports_function_calling"`
	SupportsParallelFunctionCalling *bool `json:"supports_parallel_function_calling"`
	SupportsReasoning               *bool `json:"supports_reasoning"`
	SupportsMinimalReasoningEffort  *bool `json:"supports_minimal_reasoning_effort"`
	SupportsXHighReasoningEffort    *bool `json:"supports_xhigh_reasoning_effort"`
	SupportsVision                  *bool `json:"supports_vision"`
}

func FetchModelMetadata(cfg Config) map[string]liteLLMModelMetadata {
	metadataURL := strings.TrimSpace(cfg.ModelMetadataURL)
	if metadataURL == "" || strings.EqualFold(metadataURL, "off") {
		return nil
	}

	req, err := http.NewRequest(http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := proxyHTTPClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil
	}

	if hasNestedModels(raw) {
		if catalog := decodeOpenCodeCatalog(raw); catalog != nil {
			return catalog
		}
		return nil
	}

	var catalog map[string]liteLLMModelMetadata
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil
	}
	delete(catalog, "sample_spec")
	return catalog
}

// hasNestedModels reports whether the payload uses the OpenCode catalog shape,
// where every meaningful top-level entry is a provider object carrying a
// "models" map. The LiteLLM catalog is flat: model ids are the top-level keys
// and its entries never contain a "models" field, so the two shapes are
// distinguishable.
func hasNestedModels(raw json.RawMessage) bool {
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		return false
	}
	for key, value := range topLevel {
		if key == "sample_spec" {
			continue
		}
		var entry struct {
			Models json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(value, &entry); err != nil {
			continue
		}
		if len(entry.Models) > 0 {
			return true
		}
	}
	return false
}

// openCodeCatalogProvider is a top-level entry of models.opencode.ai/api.json,
// keyed by provider id.
type openCodeCatalogProvider struct {
	Models map[string]openCodeCatalogModel `json:"models"`
}

// openCodeCatalogModel mirrors the per-model fields of the OpenCode catalog.
type openCodeCatalogModel struct {
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	ToolCall   bool `json:"tool_call"`
	Reasoning  bool `json:"reasoning"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
}

// decodeOpenCodeCatalog flattens the provider-keyed OpenCode catalog into the
// same map shape the LiteLLM parser produces. Keys are written both as the
// bare model id and as "provider/id" so findModelMetadata's existing lookup
// order can match either form.
func decodeOpenCodeCatalog(raw json.RawMessage) map[string]liteLLMModelMetadata {
	var topLevel map[string]openCodeCatalogProvider
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		return nil
	}

	catalog := make(map[string]liteLLMModelMetadata)
	for providerID, provider := range topLevel {
		if len(provider.Models) == 0 {
			continue
		}
		for modelID, model := range provider.Models {
			functionCalling := model.ToolCall
			reasoning := model.Reasoning
			supportsVision := false
			for _, modality := range model.Modalities.Input {
				if modality == "image" {
					supportsVision = true
					break
				}
			}
			metadata := liteLLMModelMetadata{
				MaxInputTokens:          model.Limit.Context,
				SupportsFunctionCalling: &functionCalling,
				SupportsReasoning:       &reasoning,
				SupportsVision:          &supportsVision,
			}
			catalog[modelID] = metadata
			catalog[providerID+"/"+modelID] = metadata
		}
	}
	if len(catalog) == 0 {
		return nil
	}
	return catalog
}

func EnrichModels(models []ModelInfo, catalog map[string]liteLLMModelMetadata, provider string) int {
	matched := 0
	for i := range models {
		metadata, ok := findModelMetadata(models[i].ID, catalog, provider)
		if !ok {
			continue
		}
		matched++
		if models[i].ContextLength <= 0 && metadata.MaxInputTokens > 0 {
			models[i].ContextLength = metadata.MaxInputTokens
		}
		models[i].Capabilities = &ModelCapabilities{
			SupportsFunctionCalling:         boolValue(metadata.SupportsFunctionCalling),
			SupportsParallelFunctionCalling: boolValue(metadata.SupportsParallelFunctionCalling),
			ReasoningSupportKnown:           metadata.SupportsReasoning != nil,
			SupportsReasoning:               boolValue(metadata.SupportsReasoning),
			SupportsMinimalReasoningEffort:  boolValue(metadata.SupportsMinimalReasoningEffort),
			SupportsXHighReasoningEffort:    boolValue(metadata.SupportsXHighReasoningEffort),
			SupportsVision:                  boolValue(metadata.SupportsVision),
		}
	}
	return matched
}

func findModelMetadata(modelID string, catalog map[string]liteLLMModelMetadata, provider string) (liteLLMModelMetadata, bool) {
	if len(catalog) == 0 {
		return liteLLMModelMetadata{}, false
	}
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(modelID), "-free"), ":free")
	direct := []string{modelID, base}
	if provider == providerOpenRouter {
		direct = append([]string{"openrouter/" + modelID, "openrouter/" + base}, direct...)
	}
	for _, key := range direct {
		if metadata, ok := catalog[key]; ok {
			return metadata, true
		}
	}

	return liteLLMModelMetadata{}, false
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func modelMetadataSummary(matched, total int) string {
	return fmt.Sprintf("matched LiteLLM metadata for %d of %d models", matched, total)
}
