package proxy

import (
	"strings"
	"testing"
)

func TestDashboardUsesTextualSelectionAndStatus(t *testing.T) {
	model := dashboardModel{
		config: dashboardConfig{Address: "127.0.0.1:8788", Provider: "zen", Model: "test-model", ModelCount: 1},
		traces: []RequestTrace{{
			ID:               7,
			Model:            "test-model",
			Inbound:          TracePayload{Method: "POST", URL: "/v1/responses"},
			UpstreamResponse: TracePayload{Status: 200},
		}},
		width: 80, height: 24,
	}

	view := model.View()
	if !strings.Contains(view, "> #7") {
		t.Fatalf("selected request lacks text marker:\n%s", view)
	}
	if !strings.Contains(view, "[WAIT]") || !strings.Contains(view, "client pending | upstream 200") {
		t.Fatalf("pending lifecycle is ambiguous:\n%s", view)
	}
}

func TestDashboardFollowsNewestTraceAcrossHistoryRollover(t *testing.T) {
	traces := make([]RequestTrace, debugHistoryLimit)
	for i := range traces {
		traces[i].ID = uint64(i + 1)
	}
	model := dashboardModel{traces: traces, selected: len(traces) - 1, width: 80, height: 24}

	updated, _ := model.Update(traceUpdateMsg{trace: RequestTrace{ID: debugHistoryLimit + 1}, ok: true})
	result := updated.(dashboardModel)
	if result.selected != len(result.traces)-1 || result.traces[result.selected].ID != debugHistoryLimit+1 {
		t.Fatalf("selected = %d, trace ID = %d; want newest ID %d", result.selected, result.traces[result.selected].ID, debugHistoryLimit+1)
	}
}
