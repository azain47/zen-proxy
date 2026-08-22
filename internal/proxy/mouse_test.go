package proxy

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func mouseClick(t *testing.T, model dashboardModel, x, y int) dashboardModel {
	t.Helper()
	model = updateDashboard(t, model, tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	return updateDashboard(t, model, tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
}

func TestMouseClickSelectsRequestRow(t *testing.T) {
	traces := make([]RequestTrace, 5)
	for i := range traces {
		traces[i] = RequestTrace{
			ID:        uint64(i + 1),
			StartedAt: time.Now(),
			Inbound:   TracePayload{Method: "POST", URL: "/v1/messages", Body: `{"a":1}`},
		}
	}
	model := dashboardModel{
		config: dashboardConfig{Address: "127.0.0.1:8788", Provider: "zen", ModelCount: 1},
		traces: traces,
		width:  100,
		height: 30,
	}

	// Wide layout: header 1 row, left list content at y>=2; display row j at y = 3 + 2j.
	clicked := mouseClick(t, model, 5, 7)
	if clicked.selected != 2 {
		t.Fatalf("request-row click selected = %d, want 2", clicked.selected)
	}
}

func TestMouseClickSwitchesPanelTab(t *testing.T) {
	model := dashboardModel{
		config: dashboardConfig{Address: "127.0.0.1:8788", Provider: "zen", ModelCount: 1},
		traces: []RequestTrace{{
			ID:      1,
			Inbound: TracePayload{Method: "POST", URL: "/v1/messages", Body: `{"a":1}`},
		}},
		width:  100,
		height: 30,
	}

	// Detail tab bar is line index 4 -> y = 6; "3:UPSTREAM RESPONSE" spans contentX [25,46).
	clicked := mouseClick(t, model, 65, 6)
	if clicked.panel != 2 {
		t.Fatalf("panel-tab click panel = %d, want 2", clicked.panel)
	}
}

func TestMouseClickFoldsAndSelectsJSONNode(t *testing.T) {
	model := dashboardModel{
		cache: &viewerCache{},
		traces: []RequestTrace{{
			ID:      1,
			Inbound: TracePayload{Method: "POST", URL: "/v1/messages", Body: `{"message":{"role":"user","content":"hi"}}`},
		}},
		width:  100,
		height: 30,
	}

	width := 96
	_ = model.traceDetailLines(width)

	target := -1
	for i, entry := range model.cache.detailNodePaths {
		if path, ok := decodeNodePath(entry); ok && path == "/message" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatal("no detail line mapped to JSON node /message")
	}

	model.handleDetailClick(0, target, width)
	if model.jsonCursor != "/message" {
		t.Fatalf("click selected cursor = %q, want /message", model.jsonCursor)
	}
	node, ok := model.selectedJSONNode()
	if !ok || !node.Collapsed {
		t.Fatalf("expected clicked container to collapse, ok=%v", ok)
	}

	model.handleDetailClick(0, target, width)
	node, ok = model.selectedJSONNode()
	if !ok || node.Collapsed {
		t.Fatal("expected second click to expand the container")
	}
}

func TestExtractSelectionReturnsPlainRectangle(t *testing.T) {
	styled := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF0000")).Render("copy me")
	frame := "hello world" + "\n" + styled + "\n" + "ignored row"

	got := extractSelection(frame, 0, 0, 3, 1)
	want := "hell\ncopy"
	if got != want {
		t.Fatalf("extractSelection = %q, want %q", got, want)
	}
}

func TestMouseDragCopiesSelection(t *testing.T) {
	model := dashboardModel{
		config: dashboardConfig{Address: "127.0.0.1:8788", Provider: "zen", ModelCount: 1},
		traces: []RequestTrace{{
			ID:      1,
			Inbound: TracePayload{Method: "POST", URL: "/v1/messages", Body: `{"a":1}`},
		}},
		width:  100,
		height: 30,
	}

	model = updateDashboard(t, model, tea.MouseMsg{X: 2, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	model = updateDashboard(t, model, tea.MouseMsg{X: 8, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	if !model.selecting {
		t.Fatal("expected drag to enter selecting state")
	}

	release := tea.MouseMsg{X: 8, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}
	updated, cmd := model.Update(release)
	model = updated.(dashboardModel)
	if model.selecting {
		t.Fatal("expected release to end selecting state")
	}
	if cmd == nil {
		t.Fatal("expected release drag to emit a clipboard copy command")
	}
	msg := cmd()
	result, ok := msg.(clipboardResultMsg)
	if !ok {
		t.Fatalf("copy command message = %T, want clipboardResultMsg", msg)
	}
	if !strings.HasPrefix(result.label, "selection (") {
		t.Fatalf("copy label = %q, want selection (...)", result.label)
	}
}
