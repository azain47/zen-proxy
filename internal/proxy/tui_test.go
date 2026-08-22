package proxy

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestDashboardFitsCommonTerminalSizes(t *testing.T) {
	sizes := []struct {
		width  int
		height int
	}{{20, 5}, {30, 8}, {40, 12}, {48, 12}, {60, 24}, {72, 24}, {80, 24}, {96, 24}, {120, 24}}
	for _, size := range sizes {
		name := strconv.Itoa(size.width) + "x" + strconv.Itoa(size.height)
		t.Run(name, func(t *testing.T) {
			model := dashboardModel{
				config: dashboardConfig{Address: "127.0.0.1:8788", Provider: "zen", Model: "deepseek-v4-flash-free", ModelCount: 55},
				traces: []RequestTrace{{
					ID: 1, StartedAt: time.Now(), Duration: time.Second,
					Protocol: "openai-responses", Model: "deepseek-v4-flash-free",
					Inbound: TracePayload{
						Method: "POST", URL: "/v1/responses?client_version=0.144.1",
						Headers: map[string]string{"X-Client-Request-Id": "019f5318-2b92-7be2-8342-e4ec62038caf", "User-Agent": "codex_exec/0.144.1 (Mac OS; arm64)"},
						Body:    "{\"model\":\"deepseek-v4-flash-free\",\"input\":\"hello\"}",
					},
					UpstreamResponse: TracePayload{Status: 200},
					Response:         TracePayload{Status: 200},
				}},
				width:  size.width,
				height: size.height,
			}
			view := model.View()
			if !strings.Contains(view, "REQUESTS 1/1") && size.width >= 72 {
				t.Fatalf("request count missing:\n%s", view)
			}
			lines := strings.Split(view, "\n")
			if len(lines) > model.height {
				t.Fatalf("view height = %d, terminal height = %d", len(lines), model.height)
			}
			for lineNumber, line := range lines {
				if got := lipgloss.Width(line); got > size.width {
					t.Fatalf("line %d width = %d, terminal width = %d\n%s", lineNumber+1, got, size.width, line)
				}
			}
		})
	}
}

func TestDashboardScrollingUsesBoundedDetailOffset(t *testing.T) {
	model := dashboardModel{
		config: dashboardConfig{Address: "127.0.0.1:8788", Provider: "zen", ModelCount: 55},
		traces: []RequestTrace{{
			ID: 1, StartedAt: time.Now(), Duration: time.Second,
			Protocol: "openai-responses", Model: "deepseek-v4-flash-free",
			Inbound: TracePayload{
				Method: "POST", URL: "/v1/responses",
				Body: strings.Repeat("payload line\n", 100),
			},
			UpstreamResponse: TracePayload{Status: 200},
			Response:         TracePayload{Status: 200},
		}},
		width:  80,
		height: 24,
	}

	maximum := model.maxScroll()
	if maximum <= 0 {
		t.Fatal("long payload should require scrolling")
	}

	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyDown})
	if model.scroll != 3 {
		t.Fatalf("down scroll = %d, want 3", model.scroll)
	}

	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyPgDown})
	if model.scroll <= 3 {
		t.Fatalf("page down scroll = %d, want > 3", model.scroll)
	}

	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyEnd})
	if model.scroll != maximum {
		t.Fatalf("end scroll = %d, want %d", model.scroll, maximum)
	}

	model = updateDashboard(t, model, tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if model.scroll != maximum {
		t.Fatalf("wheel down exceeded maximum: %d > %d", model.scroll, maximum)
	}

	model = updateDashboard(t, model, tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if model.scroll != maximum-6 {
		t.Fatalf("wheel up scroll = %d, want %d", model.scroll, maximum-6)
	}

	model.scroll = maximum + 1000
	model.clampScroll()
	if model.scroll != maximum {
		t.Fatalf("stale offset = %d, want clamped to %d", model.scroll, maximum)
	}

	model = updateDashboard(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if model.scroll != 0 {
		t.Fatalf("panel switch scroll = %d, want 0", model.scroll)
	}
}

func updateDashboard(t *testing.T, model dashboardModel, message tea.Msg) dashboardModel {
	t.Helper()
	updated, _ := model.Update(message)
	result, ok := updated.(dashboardModel)
	if !ok {
		t.Fatalf("updated model type = %T", updated)
	}
	return result
}
