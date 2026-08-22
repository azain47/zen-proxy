package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	providerZen        = "zen"
	providerOpenRouter = "openrouter"

	defaultZenUpstream        = "https://opencode.ai/zen/v1/chat/completions"
	defaultOpenRouterUpstream = "https://openrouter.ai/api/v1/chat/completions"
	defaultOpenRouterModels   = "https://openrouter.ai/api/v1/models"
)

type Config struct {
	Port              string
	Host              string
	Provider          string
	Upstream          string
	ModelsURL         string
	ModelMetadataURL  string
	APIKey            string
	Model             string
	ModelConfigured   bool
	HTTPReferer       string
	AppTitle          string
	CORSOrigins       []string
	CodexInstructions string
	Debugger          *Debugger
}

type ModelInfo struct {
	ID            string             `json:"id"`
	Object        string             `json:"object"`
	OwnedBy       string             `json:"owned_by"`
	ContextLength int                `json:"context_length,omitempty"`
	Pricing       *ModelPricing      `json:"pricing,omitempty"`
	Architecture  *ModelArchitecture `json:"architecture,omitempty"`
	Capabilities  *ModelCapabilities `json:"-"`
}

type ModelPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

type ModelArchitecture struct {
	OutputModalities []string `json:"output_modalities"`
}

func LoadConfig() Config {
	provider := normalizeProvider(envOr("ZEN_PROVIDER", providerZen))
	cfg := Config{
		Port:             envOr("ZEN_PORT", "8788"),
		Host:             envOr("ZEN_HOST", "127.0.0.1"),
		Provider:         provider,
		ModelMetadataURL: strings.TrimSpace(os.Getenv("ZEN_MODEL_METADATA_URL")),
		CORSOrigins:      splitCommaList(os.Getenv("ZEN_CORS_ORIGINS")),
	}

	switch provider {
	case providerOpenRouter:
		cfg.Upstream = envOr("ZEN_UPSTREAM", defaultOpenRouterUpstream)
		cfg.ModelsURL = envOr("ZEN_MODELS_URL", defaultOpenRouterModels)
		cfg.APIKey = envFirst("ZEN_API_KEY", "OPENROUTER_API_KEY")
		cfg.Model = envOr("ZEN_MODEL", "openrouter/free")
		cfg.HTTPReferer = envFirst("OPENROUTER_HTTP_REFERER", "ZEN_HTTP_REFERER")
		cfg.AppTitle = envFirst("OPENROUTER_APP_TITLE", "ZEN_APP_TITLE")
		if cfg.AppTitle == "" {
			cfg.AppTitle = "zen-proxy"
		}
	default:
		cfg.Upstream = envOr("ZEN_UPSTREAM", defaultZenUpstream)
		cfg.ModelsURL = envOr("ZEN_MODELS_URL", "")
		cfg.APIKey = envOr("ZEN_API_KEY", "public")
		cfg.Model = envOr("ZEN_MODEL", "deepseek-v4-flash-free")
	}
	cfg.ModelConfigured = strings.TrimSpace(os.Getenv("ZEN_MODEL")) != ""

	cfg.CodexInstructions = loadCodexInstructions()

	return cfg
}

func loadCodexInstructions() string {
	path := os.Getenv("ZEN_PROXY_CODEX_INSTRUCTIONS_FILE")
	if path == "" {
		return codexBaseInstructions
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("warning: could not read ZEN_PROXY_CODEX_INSTRUCTIONS_FILE=%q (%v); using embedded Codex prompt", path, err)
		return codexBaseInstructions
	}
	return string(data)
}

func FetchModels(cfg Config) []ModelInfo {
	modelsURL := cfg.ModelsURL
	if modelsURL == "" {
		modelsURL = strings.TrimSuffix(cfg.Upstream, "/chat/completions") + "/models"
	}

	req, err := http.NewRequest("GET", modelsURL, nil)
	if err != nil {
		log.Printf("warning: failed to build models request: %v", err)
		return nil
	}
	setUpstreamHeaders(req, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := proxyHTTPClient.Do(req.WithContext(ctx))
	if err != nil {
		log.Printf("warning: failed to fetch models: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("warning: models endpoint returned %d", resp.StatusCode)
		return nil
	}

	var result struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("warning: failed to decode models: %v", err)
		return nil
	}
	for i := range result.Data {
		if result.Data[i].Object == "" {
			result.Data[i].Object = "model"
		}
		if result.Data[i].OwnedBy == "" {
			result.Data[i].OwnedBy = providerOwner(cfg.Provider)
		}
	}
	return result.Data
}

func resolveFallbackModel(cfg Config, models []ModelInfo) string {
	if cfg.ModelConfigured || len(models) == 0 {
		return cfg.Model
	}
	for _, model := range models {
		if model.ID == cfg.Model {
			return cfg.Model
		}
	}

	candidate := firstCurrentFreeModel(models)
	if candidate == "" && cfg.Provider != providerOpenRouter {
		candidate = firstModelID(models)
	}
	if candidate == "" {
		log.Printf("warning: default model %q is not in the upstream model list; set ZEN_MODEL to a current model", cfg.Model)
		return cfg.Model
	}
	log.Printf("warning: default model %q is not in the upstream model list; using current model %q", cfg.Model, candidate)
	return candidate
}

func firstCurrentFreeModel(models []ModelInfo) string {
	for _, model := range models {
		if model.ID != "" && isFreeModel(model) && hasTextOnlyOutput(model) {
			return model.ID
		}
	}
	return ""
}

func firstModelID(models []ModelInfo) string {
	for _, model := range models {
		if model.ID != "" {
			return model.ID
		}
	}
	return ""
}

type modelStats struct {
	Total int
	Free  int
	Other int
}

func summarizeModels(provider string, models []ModelInfo) modelStats {
	stats := modelStats{Total: len(models)}
	for _, model := range models {
		free := isFreeModel(model)
		if provider == providerOpenRouter {
			free = free && hasTextOnlyOutput(model)
		}
		if free {
			stats.Free++
		} else {
			stats.Other++
		}
	}
	return stats
}

func writeModelList(w io.Writer, provider string, models []ModelInfo) {
	stats := summarizeModels(provider, models)
	freeLabel := "Free"
	if provider == providerOpenRouter {
		freeLabel = "Free text"
	}
	fmt.Fprintf(w, "Models (%d)\n\n%s (%d)\n", stats.Total, freeLabel, stats.Free)
	for _, model := range models {
		free := isFreeModel(model)
		if provider == providerOpenRouter {
			free = free && hasTextOnlyOutput(model)
		}
		if model.ID != "" && free {
			fmt.Fprintf(w, "  - %s\n", model.ID)
		}
	}
	fmt.Fprintf(w, "\nOther (%d)\n", stats.Other)
	for _, model := range models {
		free := isFreeModel(model)
		if provider == providerOpenRouter {
			free = free && hasTextOnlyOutput(model)
		}
		if model.ID != "" && !free {
			fmt.Fprintf(w, "  - %s\n", model.ID)
		}
	}
}

func isFreeModel(m ModelInfo) bool {
	id := strings.ToLower(m.ID)
	if strings.HasSuffix(id, "-free") || strings.HasSuffix(id, ":free") || id == "big-pickle" || id == "openrouter/free" {
		return true
	}
	if m.Pricing == nil {
		return false
	}
	return priceIsZero(m.Pricing.Prompt) && priceIsZero(m.Pricing.Completion)
}

func hasTextOnlyOutput(m ModelInfo) bool {
	if m.Architecture == nil || len(m.Architecture.OutputModalities) == 0 {
		return true
	}
	for _, modality := range m.Architecture.OutputModalities {
		if strings.ToLower(modality) != "text" {
			return false
		}
	}
	return true
}

func priceIsZero(price string) bool {
	price = strings.TrimSpace(price)
	if price == "" {
		return false
	}
	value, err := strconv.ParseFloat(price, 64)
	return err == nil && value == 0
}

func normalizeProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openrouter", "open-router", "or":
		return providerOpenRouter
	default:
		return providerZen
	}
}

func providerOwner(provider string) string {
	if provider == providerOpenRouter {
		return providerOpenRouter
	}
	return "opencode"
}

func envFirst(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCommaList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}
