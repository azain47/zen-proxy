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

	var catalog map[string]liteLLMModelMetadata
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil
	}
	delete(catalog, "sample_spec")
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
