package proxy

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteStartupSummaryUsesCompactHierarchy(t *testing.T) {
	var output bytes.Buffer
	writeStartupSummary(&output, startupInfo{
		Version:  "v1.2.3",
		Provider: providerZen,
		Upstream: "https://example.test/chat/completions",
		Model:    "current-free",
		Address:  "http://127.0.0.1:8788",
		Models:   modelStats{Total: 5, Free: 2, Other: 3},
	})

	want := "zen-proxy v1.2.3\n" +
		"  ready    http://127.0.0.1:8788\n" +
		"  route    zen -> https://example.test/chat/completions\n" +
		"  model    current-free\n" +
		"  catalog  5 models (2 free, 3 other)\n" +
		"  inspect  zen-proxy --tui\n"
	if output.String() != want {
		t.Fatalf("startup summary:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestWriteModelListGroupsOpenRouterModels(t *testing.T) {
	models := []ModelInfo{
		{ID: "free/text", Pricing: &ModelPricing{Prompt: "0", Completion: "0"}},
		{ID: "free/audio", Pricing: &ModelPricing{Prompt: "0", Completion: "0"}, Architecture: &ModelArchitecture{OutputModalities: []string{"audio"}}},
		{ID: "paid/text", Pricing: &ModelPricing{Prompt: "0.1", Completion: "0.1"}},
	}
	var output bytes.Buffer
	writeModelList(&output, providerOpenRouter, models)

	text := output.String()
	if !strings.Contains(text, "Free text (1)\n  - free/text") {
		t.Fatalf("free text group missing:\n%s", text)
	}
	if !strings.Contains(text, "Other (2)\n  - free/audio\n  - paid/text") {
		t.Fatalf("other group missing:\n%s", text)
	}
}

func TestParseRunOptionsSupportsModelsAndNoColor(t *testing.T) {
	_, command, err := parseRunOptions([]string{"models"})
	if err != nil || command != "models" {
		t.Fatalf("models command = %q, err = %v", command, err)
	}

	t.Setenv("NO_COLOR", "")
	options, command, err := parseRunOptions(nil)
	if err != nil || command != "" || !options.NoColor {
		t.Fatalf("options = %#v, command = %q, err = %v", options, command, err)
	}
}
