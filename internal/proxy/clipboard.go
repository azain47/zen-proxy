package proxy

import (
	"fmt"
	"os"
	"strings"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
)

func (m dashboardModel) jsonNodeMarkdown() (string, bool) {
	if _, ok := m.currentPayload(); !ok {
		return "", false
	}
	if _, ok := m.currentJSONTree(); !ok {
		return "", false
	}
	node, ok := m.selectedJSONNode()
	if !ok {
		return "", false
	}

	body := prettyJSONTree(node)
	path := node.Path
	if path == "" {
		path = "/"
	}
	label := node.Key
	if label == "" {
		label = "root"
	}
	var markdown strings.Builder
	fmt.Fprintf(&markdown, "# JSON node `%s`\n\n", path)
	fmt.Fprintf(&markdown, "- Key: `%s`\n", label)
	fmt.Fprintf(&markdown, "- Source panel: `%s`\n\n", panelLabel(m.panel))
	fence := markdownFence(body)
	fmt.Fprintf(&markdown, "%sjson\n%s\n%s\n", fence, body, fence)
	return markdown.String(), true
}

func panelLabel(panel int) string {
	switch panel {
	case 1:
		return "translated upstream request"
	case 2:
		return "upstream response"
	case 3:
		return "response to client"
	default:
		return "inbound request"
	}
}

func (m dashboardModel) panelMarkdown() (string, bool) {
	if m.showModels {
		return m.modelsMarkdown(), true
	}
	trace, ok := m.selectedTrace()
	if !ok {
		return "", false
	}
	payload, label := tracePanel(trace, m.panel)
	var markdown strings.Builder
	markdown.WriteString("# " + label + "\n\n")
	if !appendPayloadMarkdown(&markdown, m, payload) {
		return "", false
	}
	return markdown.String(), true
}

// modelsMarkdown renders the model catalogue as Markdown so copying from the
// models view produces the list of models rather than a trace payload.
func (m dashboardModel) modelsMarkdown() string {
	free, paid := m.partitionModels()
	var markdown strings.Builder
	fmt.Fprintf(&markdown, "# Models (%d)\n\n", len(m.config.Models))
	fmt.Fprintf(&markdown, "- Provider: `%s`\n", m.config.Provider)
	fmt.Fprintf(&markdown, "- Default: `%s`\n\n", emptyAs(m.config.Model, "unavailable"))
	write := func(title string, models []ModelInfo) {
		fmt.Fprintf(&markdown, "## %s (%d)\n\n", title, len(models))
		if len(models) == 0 {
			markdown.WriteString("_none reported_\n\n")
			return
		}
		for _, model := range models {
			fmt.Fprintf(&markdown, "- `%s` — %s\n", model.ID, modelDetail(model))
		}
		markdown.WriteString("\n")
	}
	write("Free", free)
	write("Other", paid)
	return markdown.String()
}

func (m dashboardModel) traceMarkdown() (string, bool) {
	trace, ok := m.selectedTrace()
	if !ok {
		return "", false
	}

	var markdown strings.Builder
	fmt.Fprintf(&markdown, "# Trace #%d\n\n", trace.ID)
	if trace.Protocol != "" {
		fmt.Fprintf(&markdown, "- Protocol: %s\n", trace.Protocol)
	}
	if trace.Model != "" {
		fmt.Fprintf(&markdown, "- Model: %s\n", trace.Model)
	}
	if trace.Duration > 0 {
		fmt.Fprintf(&markdown, "- Duration: %s\n", traceDuration(trace))
	}
	if trace.Error != "" {
		fmt.Fprintf(&markdown, "- Error: %s\n", trace.Error)
	}
	markdown.WriteString("\n")

	labels := []string{"1. Inbound request", "2. Translated upstream request", "3. Upstream response", "4. Response to client"}
	payloads := []TracePayload{trace.Inbound, trace.Upstream, trace.UpstreamResponse, trace.Response}
	copied := false
	for i := range payloads {
		markdown.WriteString("## " + labels[i] + "\n\n")
		copied = appendPayloadMarkdown(&markdown, m, payloads[i]) || copied
	}
	return markdown.String(), copied
}

func (m dashboardModel) selectedTrace() (RequestTrace, bool) {
	if len(m.traces) == 0 || m.selected < 0 || m.selected >= len(m.traces) {
		return RequestTrace{}, false
	}
	return m.traces[m.selected], true
}

func appendPayloadMarkdown(markdown *strings.Builder, model dashboardModel, payload TracePayload) bool {
	copied := false
	if payload.Method != "" || payload.URL != "" || payload.Status != 0 {
		if payload.Method != "" || payload.URL != "" {
			fmt.Fprintf(markdown, "**Request:** `%s %s`  \n", payload.Method, payload.URL)
		}
		if payload.Status != 0 {
			fmt.Fprintf(markdown, "**Status:** `%d`  \n", payload.Status)
		}
		copied = true
	}

	if headers := traceHeaders(payload.Headers); headers != "" {
		markdown.WriteString("\n### Headers\n\n")
		fence := markdownFence(headers)
		fmt.Fprintf(markdown, "%stext\n%s\n%s\n", fence, headers, fence)
		copied = true
	}
	if payload.Body != "" {
		body, language := payload.Body, "text"
		if formatted, ok := model.formattedPayload(payload.Body); ok {
			body, language = formatted, "json"
		}
		markdown.WriteString("\n### Body\n\n")
		fence := markdownFence(body)
		fmt.Fprintf(markdown, "%s%s\n%s\n%s\n", fence, language, body, fence)
		copied = true
	}
	if payload.Truncated {
		markdown.WriteString("\n> Payload source marked this body as truncated.\n")
	}
	if copied {
		markdown.WriteString("\n")
	}
	return copied
}

func markdownFence(body string) string {
	longest, current := 0, 0
	for _, char := range body {
		if char == rune(0x60) {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return strings.Repeat(string(rune(0x60)), max(3, longest+1))
}

func copyMarkdownCmd(markdown, label string) tea.Cmd {
	return func() tea.Msg {
		sequence := osc52.New(markdown)
		switch {
		case os.Getenv("TMUX") != "":
			sequence = sequence.Tmux()
		case strings.HasPrefix(strings.ToLower(os.Getenv("TERM")), "screen"):
			sequence = sequence.Screen()
		}
		_, err := fmt.Fprint(os.Stderr, sequence)
		return clipboardResultMsg{err: err, label: label}
	}
}
