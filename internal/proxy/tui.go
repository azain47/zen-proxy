package proxy

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type dashboardConfig struct {
	Address    string
	Provider   string
	ModelCount int
}

type traceUpdateMsg struct {
	trace RequestTrace
	ok    bool
}

type dashboardModel struct {
	config dashboardConfig
	events <-chan RequestTrace
	traces []RequestTrace

	selected int
	panel    int
	scroll   int
	width    int
	height   int
}

var (
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#5EEAD4")).Bold(true)
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#94A3B8"))
	borderStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#334155"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#86EFAC")).Bold(true)
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FCD34D")).Bold(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FDA4AF")).Bold(true)
	selectStyle = lipgloss.NewStyle().Background(lipgloss.Color("#164E63")).Foreground(lipgloss.Color("#ECFEFF"))
)

func RunDashboard(config dashboardConfig, debugger *Debugger) error {
	events, unsubscribe := debugger.Subscribe()
	defer unsubscribe()

	model := dashboardModel{
		config: config,
		events: events,
		traces: debugger.Snapshot(),
	}
	if len(model.traces) > 0 {
		model.selected = len(model.traces) - 1
	}
	_, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func (m dashboardModel) Init() tea.Cmd {
	return waitForTrace(m.events)
}

func waitForTrace(events <-chan RequestTrace) tea.Cmd {
	return func() tea.Msg {
		trace, ok := <-events
		return traceUpdateMsg{trace: trace, ok: ok}
	}
}

func (m dashboardModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampScroll()
		return m, nil
	case traceUpdateMsg:
		if !msg.ok {
			return m, nil
		}
		wasAtNewest := len(m.traces) == 0 || m.selected == len(m.traces)-1
		previousCount := len(m.traces)
		m.upsert(msg.trace)
		if wasAtNewest && len(m.traces) > previousCount {
			m.selected = len(m.traces) - 1
			m.scroll = 0
		}
		m.clampScroll()
		return m, waitForTrace(m.events)
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "k":
			if m.selected > 0 {
				m.selected--
				m.scroll = 0
			}
		case "j":
			if m.selected < len(m.traces)-1 {
				m.selected++
				m.scroll = 0
			}
		case "up":
			m.scroll--
		case "down":
			m.scroll++
		case "1":
			m.panel, m.scroll = 0, 0
		case "2":
			m.panel, m.scroll = 1, 0
		case "3":
			m.panel, m.scroll = 2, 0
		case "4":
			m.panel, m.scroll = 3, 0
		case "pgdown", "ctrl+d":
			m.scroll += m.pageScrollStep()
		case "pgup", "ctrl+u":
			m.scroll -= m.pageScrollStep()
		case "home", "g":
			m.scroll = 0
		case "end", "G":
			m.scroll = m.maxScroll()
		}
		m.clampScroll()
	case tea.MouseMsg:
		event := tea.MouseEvent(msg)
		switch event.Button {
		case tea.MouseButtonWheelUp:
			m.scroll -= 3
		case tea.MouseButtonWheelDown:
			m.scroll += 3
		}
		m.clampScroll()
	}
	return m, nil
}

func (m *dashboardModel) upsert(trace RequestTrace) {
	for i := range m.traces {
		if m.traces[i].ID == trace.ID {
			m.traces[i] = trace
			return
		}
	}
	m.traces = append(m.traces, trace)
	if len(m.traces) > debugHistoryLimit {
		m.traces = m.traces[len(m.traces)-debugHistoryLimit:]
		m.selected = max(0, m.selected-1)
	}
}

func (m dashboardModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "Starting zen-proxy dashboard..."
	}

	contentHeight := max(8, m.height-6)
	header := accentStyle.Render("ZEN PROXY") + mutedStyle.Render("  live request inspector") +
		mutedStyle.Render(fmt.Sprintf("  %s  |  %d models  |  %s", m.config.Address, m.config.ModelCount, m.config.Provider))
	footer := mutedStyle.Render("j/k requests | arrows/page scroll | wheel | 1-4 panels | q quit")

	if m.width < 72 {
		header = accentStyle.Render("ZEN PROXY") + mutedStyle.Render(fmt.Sprintf("  %s  |  %s", m.config.Address, m.config.Provider))
		footer = mutedStyle.Render("arrows/wheel scroll | 1-4 panels | q quit")
		panelWidth := max(20, m.width-2)
		detail := borderStyle.Width(panelWidth).Height(contentHeight).Render(m.traceDetail(panelWidth, contentHeight-2))
		return lipgloss.JoinVertical(lipgloss.Left, header, detail, footer)
	}

	leftWidth := min(42, max(26, m.width/3))
	rightWidth := max(20, m.width-leftWidth-5)
	left := borderStyle.Width(leftWidth).Height(contentHeight).Render(m.requestList(leftWidth, contentHeight-2))
	right := borderStyle.Width(rightWidth).Height(contentHeight).Render(m.traceDetail(rightWidth, contentHeight-2))

	return lipgloss.JoinVertical(lipgloss.Left, header, lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right), footer)
}

func (m dashboardModel) requestList(width, height int) string {
	position := 0
	if len(m.traces) > 0 {
		position = m.selected + 1
	}
	lines := []string{accentStyle.Render(fmt.Sprintf("REQUESTS  %d/%d", position, len(m.traces))), ""}
	if len(m.traces) == 0 {
		return strings.Join(append(lines, mutedStyle.Render("Waiting for Codex, Claude, or another client...")), "\n")
	}

	rows := max(1, height-len(lines))
	start := max(0, min(m.selected-rows/2, len(m.traces)-rows))
	end := min(len(m.traces), start+rows)
	for i := start; i < end; i++ {
		trace := m.traces[i]
		status := trace.Response.Status
		if status == 0 {
			status = trace.UpstreamResponse.Status
		}
		label := fmt.Sprintf("%s  %s  %s", trace.StartedAt.Format("15:04:05"), statusLabel(status), shortModel(trace.Model, 17))
		detail := fmt.Sprintf("#%-3d %s %s", trace.ID, trace.Inbound.Method, trace.Inbound.URL)
		row := fitText(label, width)
		row += "\n" + mutedStyle.Render(fitText(detail, width))
		if i == m.selected {
			row = selectStyle.Width(width).Render(row)
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

func (m dashboardModel) traceDetail(width, height int) string {
	all := m.traceDetailLines(width)
	scroll := min(max(0, m.scroll), max(0, len(all)-height))
	return strings.Join(all[scroll:min(len(all), scroll+height)], "\n")
}

func (m dashboardModel) traceDetailLines(width int) []string {
	if len(m.traces) == 0 || m.selected < 0 || m.selected >= len(m.traces) {
		return []string{accentStyle.Render("LIVE TRAFFIC"), "", mutedStyle.Render("Requests appear here after they reach the proxy.")}
	}

	trace := m.traces[m.selected]
	payload, label := tracePanel(trace, m.panel)
	status := trace.Response.Status
	if status == 0 {
		status = trace.UpstreamResponse.Status
	}

	lines := []string{
		accentStyle.Render(fmt.Sprintf("#%d  %s", trace.ID, strings.ToUpper(label))),
		statusLabel(status) + " " + wrapText(trace.Inbound.Method+" "+trace.Inbound.URL, max(8, width-4)),
		mutedStyle.Render(wrapText(fmt.Sprintf("protocol: %s   model: %s   duration: %s", emptyAs(trace.Protocol, "pending"), emptyAs(trace.Model, "pending"), trace.Duration.Round(time.Millisecond)), width)),
	}
	if trace.Error != "" {
		lines = append(lines, errStyle.Render(wrapText("transport error: "+trace.Error, width)))
	}
	lines = append(lines, "", mutedStyle.Render("headers"), wrapText(traceHeaders(payload.Headers), width), "", mutedStyle.Render("body"))
	if payload.Body == "" {
		lines = append(lines, mutedStyle.Render("(empty)"))
	} else {
		lines = append(lines, wrapText(payload.Body, width))
	}
	if payload.Truncated {
		lines = append(lines, warnStyle.Render(fmt.Sprintf("Payload preview truncated at %d KiB.", debugBodyLimit/1024)))
	}

	return strings.Split(strings.Join(lines, "\n"), "\n")
}

func (m dashboardModel) detailDimensions() (int, int) {
	contentHeight := max(8, m.height-6)
	if m.width < 72 {
		return max(20, m.width-2), contentHeight - 2
	}
	leftWidth := min(42, max(26, m.width/3))
	return max(20, m.width-leftWidth-5), contentHeight - 2
}

func (m dashboardModel) maxScroll() int {
	width, height := m.detailDimensions()
	return max(0, len(m.traceDetailLines(width))-height)
}

func (m dashboardModel) pageScrollStep() int {
	_, height := m.detailDimensions()
	return max(1, height-2)
}

func (m *dashboardModel) clampScroll() {
	m.scroll = min(max(0, m.scroll), m.maxScroll())
}

func tracePanel(trace RequestTrace, panel int) (TracePayload, string) {
	switch panel {
	case 1:
		return trace.Upstream, "translated upstream request"
	case 2:
		return trace.UpstreamResponse, "upstream response"
	case 3:
		return trace.Response, "response to client"
	default:
		return trace.Inbound, "inbound request"
	}
}

func statusLabel(status int) string {
	if status == 0 {
		return mutedStyle.Render("PENDING")
	}
	text := fmt.Sprintf("%d", status)
	switch {
	case status >= 500:
		return errStyle.Render(text)
	case status >= 400:
		return warnStyle.Render(text)
	case status >= 200 && status < 300:
		return okStyle.Render(text)
	default:
		return mutedStyle.Render(text)
	}
}

func shortModel(model string, limit int) string {
	if model == "" {
		return "pending"
	}
	return truncateText(model, limit)
}

func fitText(text string, width int) string {
	return truncateText(text, max(1, width))
}

func truncateText(text string, limit int) string {
	runes := []rune(text)
	if limit <= 3 || len(runes) <= limit {
		if len(runes) > limit {
			return string(runes[:limit])
		}
		return text
	}
	return string(runes[:limit-3]) + "..."
}

func emptyAs(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func wrapText(value string, width int) string {
	if width < 8 {
		return value
	}
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		runes := []rune(line)
		for len(runes) > width {
			lines = append(lines, string(runes[:width]))
			runes = runes[width:]
		}
		lines = append(lines, string(runes))
	}
	return strings.Join(lines, "\n")
}
