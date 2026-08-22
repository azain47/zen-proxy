package proxy

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestJSONViewerFormatsAndWrapsLongLines(t *testing.T) {
	body := "{\"message\":{\"role\":\"user\",\"content\":\"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ\"},\"stream\":true,\"count\":42}"
	formatted, ok := prettyJSON(body)
	if !ok || !strings.Contains(formatted, "\n  \"message\": {") {
		t.Fatalf("formatted JSON = %q, ok = %v", formatted, ok)
	}

	model := dashboardModel{}
	lines, _, ok := model.renderPayloadBody(body, 24)
	if !ok || len(lines) <= len(strings.Split(formatted, "\n")) {
		t.Fatalf("wrapped lines = %#v, ok = %v", lines, ok)
	}
	for i, line := range lines {
		if width := lipgloss.Width(line); width > 24 {
			t.Fatalf("line %d width = %d, want <= 24: %q", i, width, line)
		}
	}
}

func TestViewerNavigationSupportsAliasesPanelsAndHorizontalPan(t *testing.T) {
	longJSON := `{"content":"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"}`
	model := dashboardModel{
		traces: []RequestTrace{
			{ID: 1, Inbound: TracePayload{Body: longJSON}},
			{ID: 2, Inbound: TracePayload{Body: longJSON}},
		},
		width: 80, height: 24,
	}

	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if model.selected != 1 {
		t.Fatalf("n selected = %d, want 1", model.selected)
	}
	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if model.selected != 0 {
		t.Fatalf("p selected = %d, want 0", model.selected)
	}
	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyTab})
	if model.panel != 1 {
		t.Fatalf("tab panel = %d, want 1", model.panel)
	}
	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyShiftTab})
	if model.panel != 0 {
		t.Fatalf("shift-tab panel = %d, want 0", model.panel)
	}
	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if model.horizontal != 0 {
		t.Fatalf("wrapped JSON panned to column %d", model.horizontal)
	}
	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !model.noWrap {
		t.Fatal("w did not disable word wrap")
	}
	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if model.horizontal == 0 {
		t.Fatal("l did not pan unwrapped JSON horizontally")
	}
	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	if model.horizontal != 0 {
		t.Fatalf("0 horizontal = %d, want 0", model.horizontal)
	}
}

func TestSanitizeBodyKeepsCompleteLargeFormattedJSON(t *testing.T) {
	const tail = "viewer-tail-marker"
	body := `{"content":"` + strings.Repeat("x", 160*1024) + tail + `"}`

	sanitized, truncated := sanitizeBody([]byte(body), false)
	if truncated {
		t.Fatal("large formatted JSON was truncated")
	}
	if !strings.Contains(sanitized, tail) {
		t.Fatal("large formatted JSON lost its tail")
	}
}
