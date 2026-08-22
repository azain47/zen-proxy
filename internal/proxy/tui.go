package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

type dashboardConfig struct {
	Address    string
	Provider   string
	Model      string
	ModelCount int
	NoColor    bool
}

type traceUpdateMsg struct {
	trace RequestTrace
	ok    bool
}

type clipboardResultMsg struct {
	err   error
	label string
}
type clearClipboardNoticeMsg struct{}
type dashboardModel struct {
	config dashboardConfig
	events <-chan RequestTrace
	traces []RequestTrace
	cache  *viewerCache
	notice string

	selected   int
	panel      int
	scroll     int
	horizontal int
	noWrap     bool
	jsonCursor string
	width      int
	height     int

	frame     int
	selecting bool
	selStartX int
	selStartY int
	selEndX   int
	selEndY   int
}

type viewerCache struct {
	detailKey       viewerCacheKey
	detailLines     []string
	detailNodePaths []string
	detailValid     bool

	body        string
	formatted   string
	json        bool
	formatValid bool
	treeBody    string
	tree        *jsonTreeNode
	treeValid   bool
}

type viewerCacheKey struct {
	traceID                      uint64
	panel, width, horizontal     int
	noWrap, truncated            bool
	jsonCursor                   string
	method, url, protocol, model string
	errorText, body, headers     string
	duration                     time.Duration
	clientStatus, upstreamStatus int
}

var (
	accentStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("#67E8F9")).Bold(true)
	mutedStyle           = lipgloss.NewStyle().Foreground(lipgloss.Color("#94A3B8"))
	borderStyle          = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#475569"))
	okStyle              = lipgloss.NewStyle().Foreground(lipgloss.Color("#86EFAC")).Bold(true)
	warnStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("#FCD34D")).Bold(true)
	errStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("#FDA4AF")).Bold(true)
	selectStyle          = lipgloss.NewStyle().Bold(true).Reverse(true)
	jsonKeyStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC"))
	jsonStringStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#A7F3D0"))
	jsonNumberStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#FDE68A"))
	jsonLiteralStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#C4B5FD"))
	jsonPunctuationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#94A3B8"))
)

func RunDashboard(config dashboardConfig, debugger *Debugger) error {
	if config.NoColor {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
	events, unsubscribe := debugger.Subscribe()
	defer unsubscribe()
	model := dashboardModel{
		config: config,
		events: events,
		traces: debugger.Snapshot(),
		cache:  &viewerCache{},
	}
	if len(model.traces) > 0 {
		model.selected = len(model.traces) - 1
	}
	_, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(waitForTrace(m.events), m.tick())
}

func waitForTrace(events <-chan RequestTrace) tea.Cmd {
	return func() tea.Msg {
		trace, ok := <-events
		return traceUpdateMsg{trace: trace, ok: ok}
	}
}

type frameMsg struct{}

func (m dashboardModel) tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return frameMsg{} })
}

func (m dashboardModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if m.cache == nil {
		m.cache = &viewerCache{}
	}
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
		m.upsert(msg.trace)
		if wasAtNewest {
			m.selected = len(m.traces) - 1
			m.scroll = 0
			m.horizontal = 0
			m.jsonCursor = ""
		}
		m.clampScroll()
		return m, waitForTrace(m.events)
	case clipboardResultMsg:
		if msg.err != nil {
			m.notice = "Clipboard copy failed"
		} else {
			m.notice = fmt.Sprintf("Copied %s", msg.label)
		}
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearClipboardNoticeMsg{} })
	case clearClipboardNoticeMsg:
		m.notice = ""
		return m, nil
	case frameMsg:
		m.frame++
		return m, m.tick()
	case tea.KeyMsg:
		m.notice = ""
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "k", "p":
			m.selectRequest(-1)
		case "j", "n":
			m.selectRequest(1)
		case "up":
			if !m.moveJSONCursor(-1) {
				m.scroll -= 3
			}
		case "down":
			if !m.moveJSONCursor(1) {
				m.scroll += 3
			}
		case "h":
			m.horizontal -= 4
		case "l":
			m.horizontal += 4
		case "left":
			if !m.collapseJSONNode() {
				m.horizontal -= 4
			}
		case "right":
			if !m.expandJSONNode() {
				m.horizontal += 4
			}
		case "enter":
			m.toggleJSONNode()
		case "0":
			m.horizontal = 0
		case "w":
			m.noWrap = !m.noWrap
			m.horizontal = 0
			m.scroll = 0
		case "y":
			markdown, ok := m.jsonNodeMarkdown()
			label := "JSON node as Markdown"
			if !ok {
				markdown, ok = m.panelMarkdown()
				label = "current panel as Markdown"
			}
			if !ok {
				m.notice = "No payload to copy"
				break
			}
			return m, copyMarkdownCmd(markdown, label)
		case "c":
			markdown, ok := m.panelMarkdown()
			if !ok {
				m.notice = "No payload to copy"
				break
			}
			return m, copyMarkdownCmd(markdown, "current panel as Markdown")
		case "Y":
			markdown, ok := m.traceMarkdown()
			if !ok {
				m.notice = "No trace payload to copy"
				break
			}
			return m, copyMarkdownCmd(markdown, "complete trace as Markdown")
		case "tab", "]":
			m.changePanel(1)
		case "shift+tab", "[":
			m.changePanel(-1)
		case "1", "2", "3", "4":
			m.panel = int(msg.String()[0] - '1')
			m.scroll = 0
			m.horizontal = 0
			m.jsonCursor = ""
		case "pgdown", "ctrl+d", "ctrl+f":
			m.scroll += m.pageScrollStep()
		case "pgup", "ctrl+u", "ctrl+b":
			m.scroll -= m.pageScrollStep()
		case "home", "g":
			m.scroll = 0
		case "end", "G":
			m.scroll = m.maxScroll()
		}
		m.clampScroll()
	case tea.MouseMsg:
		event := tea.MouseEvent(msg)
		switch {
		case event.Button == tea.MouseButtonWheelUp:
			m.scroll -= 6
		case event.Button == tea.MouseButtonWheelDown:
			m.scroll += 6
		case event.Button == tea.MouseButtonLeft && event.Action == tea.MouseActionPress:
			m.notice = ""
			m.selecting = true
			m.selStartX, m.selStartY = event.X, event.Y
			m.selEndX, m.selEndY = event.X, event.Y
		case event.Action == tea.MouseActionMotion && m.selecting:
			m.selEndX, m.selEndY = event.X, event.Y
		case event.Action == tea.MouseActionRelease && m.selecting:
			m.selecting = false
			m.selEndX, m.selEndY = event.X, event.Y
			if m.selStartX == m.selEndX && m.selStartY == m.selEndY {
				m.handleMouseClick(event.X, event.Y)
			} else if text := m.selectionText(); strings.TrimSpace(text) != "" {
				m.clampScroll()
				return m, copyMarkdownCmd(text, fmt.Sprintf("selection (%d chars)", len([]rune(text))))
			}
		}
		m.clampScroll()
	}
	return m, nil
}

func (m *dashboardModel) selectRequest(delta int) {
	if len(m.traces) == 0 {
		return
	}
	m.selected = min(max(0, m.selected+delta), len(m.traces)-1)
	m.scroll = 0
	m.horizontal = 0
	m.jsonCursor = ""
}

func (m *dashboardModel) changePanel(delta int) {
	m.panel = (m.panel + delta + 4) % 4
	m.scroll = 0
	m.horizontal = 0
	m.jsonCursor = ""
}

const panelTabsLineIndex = 4

func (m *dashboardModel) selectRequestIndex(index int) {
	if index < 0 || index >= len(m.traces) || index == m.selected {
		return
	}
	m.selected = index
	m.scroll = 0
	m.horizontal = 0
	m.jsonCursor = ""
}

// handleMouseClick maps an absolute terminal cell to the layout View() renders,
// then dispatches the click to the request list or the detail panel tabs.
func (m *dashboardModel) handleMouseClick(x, y int) {
	if len(m.traces) == 0 {
		return
	}
	bodyTop := lineCount(m.header())
	bodyHeight := max(0, m.height-bodyTop-lineCount(m.footer()))
	bodyRow := y - bodyTop
	if bodyRow < 0 || bodyRow >= bodyHeight {
		return
	}
	if m.mode() == dashboardTiny {
		m.handleDetailClick(x, bodyRow, m.width)
		return
	}
	if bodyRow == 0 || bodyRow == bodyHeight-1 {
		return
	}
	contentRow := bodyRow - 1
	if m.width < 72 {
		m.handleDetailClick(x-1, contentRow, m.width-2)
		return
	}
	leftOuter := min(38, max(26, m.width/3))
	rightOuter := m.width - leftOuter - 1
	switch {
	case x >= 1 && x <= leftOuter-2:
		m.handleRequestListClick(contentRow, bodyHeight-2)
	case x >= leftOuter+2 && x <= m.width-2:
		m.handleDetailClick(x-(leftOuter+2), contentRow, rightOuter-2)
	}
}

func (m *dashboardModel) handleRequestListClick(contentRow, listHeight int) {
	if contentRow < 1 {
		return
	}
	rows := max(1, (listHeight-1)/2)
	start := max(0, min(m.selected-rows/2, len(m.traces)-rows))
	offset := (contentRow - 1) / 2
	if offset >= rows {
		return
	}
	m.selectRequestIndex(start + offset)
}

func (m *dashboardModel) handleDetailClick(contentX, contentRow, width int) {
	detailIndex := m.scroll + contentRow
	if detailIndex == panelTabsLineIndex {
		if tab, ok := panelTabAt(width, contentX); ok && tab != m.panel {
			m.panel = tab
			m.scroll = 0
			m.horizontal = 0
			m.jsonCursor = ""
		}
		return
	}
	if m.cache == nil || detailIndex < 0 || detailIndex >= len(m.cache.detailNodePaths) {
		return
	}
	path, ok := decodeNodePath(m.cache.detailNodePaths[detailIndex])
	if !ok {
		return
	}
	m.jsonCursor = path
	m.toggleJSONNode()
}

func panelTabAt(width, x int) (int, bool) {
	labels := []string{"IN", "UP", "UP-RES", "OUT"}
	if width >= 64 {
		labels = []string{"INBOUND", "UPSTREAM", "UPSTREAM RESPONSE", "CLIENT"}
	}
	pos := 0
	for i, label := range labels {
		segment := len(fmt.Sprintf("%d:%s", i+1, label)) + 2
		if x >= pos && x < pos+segment {
			return i, true
		}
		pos += segment + 1
	}
	return 0, false
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

type dashboardMode uint8

const (
	dashboardTiny dashboardMode = iota
	dashboardNarrow
	dashboardCompact
	dashboardWide
)

func (m dashboardModel) mode() dashboardMode {
	switch {
	case m.width < 48 || m.height < 10:
		return dashboardTiny
	case m.width < 72:
		return dashboardNarrow
	case m.width < 96:
		return dashboardCompact
	default:
		return dashboardWide
	}
}

func (m dashboardModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	header := m.header()
	footer := m.footer()
	bodyHeight := max(0, m.height-lineCount(header)-lineCount(footer))
	if bodyHeight == 0 {
		return header
	}

	var body string
	if m.mode() == dashboardTiny {
		body = m.traceDetail(m.width, bodyHeight)
	} else if m.width < 72 {
		body = renderDashboardPanel(m.traceDetail(m.width-2, bodyHeight-2), m.width, bodyHeight)
	} else {
		leftOuter := min(38, max(26, m.width/3))
		rightOuter := m.width - leftOuter - 1
		left := renderDashboardPanel(m.requestList(leftOuter-2, bodyHeight-2), leftOuter, bodyHeight)
		right := renderDashboardPanel(m.traceDetail(rightOuter-2, bodyHeight-2), rightOuter, bodyHeight)
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	}
	view := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	if m.selecting {
		view = applySelectionOverlay(view, m.selStartX, m.selStartY, m.selEndX, m.selEndY)
	}
	return view
}

func (m dashboardModel) header() string {
	position := m.selectedPosition()
	if m.mode() == dashboardTiny {
		return accentStyle.Render(fitText(fmt.Sprintf("ZEN PROXY | %s | req %d/%d", m.config.Provider, position, len(m.traces)), m.width))
	}
	if m.mode() == dashboardNarrow {
		first := m.wordmarkStyle().Render("ZEN PROXY") + mutedStyle.Render(fitText(fmt.Sprintf(" | %s | req %d/%d", m.config.Provider, position, len(m.traces)), max(1, m.width-9)))
		second := mutedStyle.Render(fitText(fmt.Sprintf("%s | %d models | %s", m.config.Address, m.config.ModelCount, emptyAs(m.config.Model, "model unavailable")), m.width))
		return first + "\n" + second
	}
	return m.wordmarkStyle().Render("ZEN PROXY") + mutedStyle.Render(fitText(fmt.Sprintf(" | %s LIVE TRAFFIC | %s | %s | %d models", m.pulse(), m.config.Provider, m.config.Address, m.config.ModelCount), max(1, m.width-9)))
}

func (m dashboardModel) footer() string {
	if m.notice != "" {
		return accentStyle.Render(fitText(m.notice, m.width))
	}
	var help string
	switch m.mode() {
	case dashboardWide:
		help = "j/k request | up/down tree | left/right fold | enter | w wrap | y node | c panel | Y all | drag copy | q"
	case dashboardTiny:
		help = "j/k request | up/down tree | enter | y node | Y all | q"
	default:
		help = "j/k request | up/down tree | fold | w wrap | y node | c panel | drag copy | q"
	}
	return mutedStyle.Render(fitText(help, m.width))
}

func (m dashboardModel) selectedPosition() int {
	if len(m.traces) == 0 {
		return 0
	}
	return m.selected + 1
}

func renderDashboardPanel(content string, outerWidth, outerHeight int) string {
	if outerWidth < 3 || outerHeight < 3 {
		return content
	}
	return borderStyle.Width(outerWidth - 2).Height(outerHeight - 2).Render(content)
}

func (m dashboardModel) requestList(width, height int) string {
	lines := []string{accentStyle.Render(fitText(fmt.Sprintf("REQUESTS %d/%d", m.selectedPosition(), len(m.traces)), width))}
	if len(m.traces) == 0 {
		return strings.Join(append(lines, "", mutedStyle.Render(wrapText("[WAIT] no requests yet", width))), "\n")
	}

	rows := max(1, (height-1)/2)
	start := max(0, min(m.selected-rows/2, len(m.traces)-rows))
	end := min(len(m.traces), start+rows)
	for i := start; i < end; i++ {
		trace := m.traces[i]
		marker := "  "
		if i == m.selected {
			marker = "> "
		}
		activity := ""
		if trace.Response.Status == 0 && trace.Error == "" {
			activity = " " + m.spinner()
		}
		first := fitText(fmt.Sprintf("%s#%d %s %s %s%s", marker, trace.ID, trace.StartedAt.Format("15:04:05"), traceStateText(trace), traceDuration(trace), activity), width)
		second := fitText(fmt.Sprintf("  %s %s | %s", emptyAs(trace.Inbound.Method, "request"), emptyAs(trace.Inbound.URL, "pending"), shortModel(trace.Model, max(8, width/2))), width)
		row := first + "\n" + mutedStyle.Render(second)
		if i == m.selected {
			row = selectStyle.Render(row)
		}
		lines = append(lines, row)
	}
	return strings.Join(lines, "\n")
}

func (m dashboardModel) traceDetail(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	all := m.traceDetailLines(width)
	scroll := min(max(0, m.scroll), max(0, len(all)-height))
	return strings.Join(all[scroll:min(len(all), scroll+height)], "\n")
}

func (m dashboardModel) traceDetailLines(width int) []string {
	if len(m.traces) == 0 || m.selected < 0 || m.selected >= len(m.traces) {
		return []string{
			accentStyle.Render(fitText("[WAIT] NO REQUESTS YET", width)),
			"",
			mutedStyle.Render(wrapText("Proxy is listening at "+m.config.Address+".", width)),
			mutedStyle.Render(wrapText("Send a request to see its route through the proxy.", width)),
		}
	}

	trace := m.traces[m.selected]
	payload, label := tracePanel(trace, m.panel)
	headers := traceHeaders(payload.Headers)
	if headers == "" {
		headers = "(none)"
	}
	key := viewerCacheKey{
		traceID: trace.ID, panel: m.panel, width: width, horizontal: m.horizontal,
		noWrap: m.noWrap, truncated: payload.Truncated, jsonCursor: m.jsonCursor,
		method: trace.Inbound.Method, url: trace.Inbound.URL, protocol: trace.Protocol, model: trace.Model,
		errorText: trace.Error, body: payload.Body, headers: headers, duration: trace.Duration,
		clientStatus: trace.Response.Status, upstreamStatus: trace.UpstreamResponse.Status,
	}
	if m.cache != nil && m.cache.detailValid && m.cache.detailKey == key {
		return m.cache.detailLines
	}

	status := trace.Response.Status
	statusWidth := lipgloss.Width(statusLabel(status))
	var entries, entryPaths []string
	push := func(text, path string) {
		entries = append(entries, text)
		entryPaths = append(entryPaths, path)
	}
	push(accentStyle.Render(fitText(fmt.Sprintf("#%d %s", trace.ID, strings.ToUpper(label)), width)), "")
	push(statusLabel(status)+" "+wrapText(emptyAs(trace.Inbound.Method, "request")+" "+emptyAs(trace.Inbound.URL, "pending"), max(8, width-statusWidth-1)), "")
	push(mutedStyle.Render(wrapText(traceProgress(trace), width)), "")
	push(mutedStyle.Render(wrapText(fmt.Sprintf("protocol %s | model %s | duration %s", emptyAs(trace.Protocol, "pending"), emptyAs(trace.Model, "pending"), traceDuration(trace)), width)), "")
	push(panelTabs(m.panel, width), "")
	if trace.Error != "" {
		push(errStyle.Render(wrapText("[ERR] upstream request failed: "+trace.Error, width)), "")
	}
	push("", "")
	push(mutedStyle.Render("HEADERS"), "")
	push(wrapText(headers, width), "")
	push("", "")
	if payload.Body == "" {
		push(mutedStyle.Render("BODY"), "")
		push(mutedStyle.Render("(empty; no payload captured)"), "")
	} else {
		bodyLines, bodyPaths, isJSON := m.renderPayloadBody(payload.Body, width)
		bodyLabel := "BODY"
		if isJSON && m.noWrap {
			bodyLabel = fmt.Sprintf("BODY  JSON TREE | cursor %s | wrap off | column %d | w toggle | drag copy", m.jsonCursorPath(), m.horizontal+1)
		} else if isJSON {
			bodyLabel = fmt.Sprintf("BODY  JSON TREE | cursor %s | click fold | up/down select | drag copy | w wrap", m.jsonCursorPath())
		}
		push(mutedStyle.Render(fitText(bodyLabel, width)), "")
		for i, bodyLine := range bodyLines {
			push(bodyLine, bodyPaths[i])
		}
	}
	if payload.Truncated {
		push(warnStyle.Render("[WARN] payload source marked this body as truncated"), "")
	}
	lines := make([]string, 0, len(entries))
	nodePaths := make([]string, 0, len(entries))
	for i, entry := range entries {
		for _, segment := range strings.Split(entry, "\n") {
			lines = append(lines, segment)
			nodePaths = append(nodePaths, entryPaths[i])
		}
	}
	if m.cache != nil {
		m.cache.detailKey = key
		m.cache.detailLines = lines
		m.cache.detailNodePaths = nodePaths
		m.cache.detailValid = true
	}
	return lines
}

func (m dashboardModel) renderPayloadBody(body string, width int) ([]string, []string, bool) {
	if _, ok := m.jsonTreeFor(body); ok {
		return m.renderJSONTree(body, width)
	}
	wrapped := strings.Split(wrapText(body, width), "\n")
	return wrapped, make([]string, len(wrapped)), false
}

func (m dashboardModel) formattedPayload(body string) (string, bool) {
	if m.cache != nil && m.cache.formatValid && m.cache.body == body {
		return m.cache.formatted, m.cache.json
	}
	formatted, ok := prettyJSON(body)
	if m.cache != nil {
		m.cache.body = body
		m.cache.formatted = formatted
		m.cache.json = ok
		m.cache.formatValid = true
	}
	return formatted, ok
}

func wrapJSONLine(line string, width int) []string {
	if width <= 0 {
		return nil
	}
	runes := []rune(line)
	if len(runes) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, (len(runes)+width-1)/width)
	for len(runes) > width {
		breakAt := width
		for i := width; i > 0; i-- {
			if unicode.IsSpace(runes[i-1]) {
				breakAt = i
				break
			}
		}
		lines = append(lines, string(runes[:breakAt]))
		runes = runes[breakAt:]
	}
	lines = append(lines, string(runes))
	return lines
}

func prettyJSON(body string) (string, bool) {
	if !json.Valid([]byte(body)) {
		return "", false
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	var formatted bytes.Buffer
	encoder := json.NewEncoder(&formatted)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "", false
	}
	return strings.TrimSuffix(formatted.String(), "\n"), true
}

func horizontalSlice(value string, offset, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	offset = min(max(0, offset), len(runes))
	end := min(len(runes), offset+width)
	return string(runes[offset:end])
}

func highlightJSONLine(line string) string {
	var rendered strings.Builder
	for i := 0; i < len(line); {
		switch ch := line[i]; {
		case ch == '"':
			end := jsonStringEnd(line, i)
			token := line[i:end]
			next := end
			for next < len(line) && (line[next] == ' ' || line[next] == '\t') {
				next++
			}
			if next < len(line) && line[next] == ':' {
				rendered.WriteString(jsonKeyStyle.Render(token))
			} else {
				rendered.WriteString(jsonStringStyle.Render(token))
			}
			i = end
		case strings.ContainsRune("{}[],:", rune(ch)):
			rendered.WriteString(jsonPunctuationStyle.Render(string(ch)))
			i++
		case (ch >= '0' && ch <= '9') || ch == '-':
			end := i + 1
			for end < len(line) && strings.ContainsRune("0123456789+-.eE", rune(line[end])) {
				end++
			}
			rendered.WriteString(jsonNumberStyle.Render(line[i:end]))
			i = end
		case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z'):
			end := i + 1
			for end < len(line) && ((line[end] >= 'a' && line[end] <= 'z') || (line[end] >= 'A' && line[end] <= 'Z')) {
				end++
			}
			rendered.WriteString(jsonLiteralStyle.Render(line[i:end]))
			i = end
		default:
			rendered.WriteByte(ch)
			i++
		}
	}
	return rendered.String()
}

func jsonStringEnd(line string, start int) int {
	escaped := false
	for i := start + 1; i < len(line); i++ {
		if escaped {
			escaped = false
			continue
		}
		if line[i] == '\\' {
			escaped = true
			continue
		}
		if line[i] == '"' {
			return i + 1
		}
	}
	return len(line)
}

func panelTabs(active, width int) string {
	labels := []string{"IN", "UP", "UP-RES", "OUT"}
	if width >= 64 {
		labels = []string{"INBOUND", "UPSTREAM", "UPSTREAM RESPONSE", "CLIENT"}
	}
	parts := make([]string, len(labels))
	for i, label := range labels {
		text := fmt.Sprintf("%d:%s", i+1, label)
		if i == active {
			parts[i] = accentStyle.Render("[" + text + "]")
		} else {
			parts[i] = mutedStyle.Render(" " + text + " ")
		}
	}
	return strings.Join(parts, " ")
}

func traceStateText(trace RequestTrace) string {
	if trace.Response.Status != 0 {
		return plainStatusLabel(trace.Response.Status)
	}
	if trace.Error != "" {
		return "[ERR]"
	}
	return "[WAIT]"
}

func traceProgress(trace RequestTrace) string {
	client := "pending"
	if trace.Response.Status != 0 {
		client = fmt.Sprintf("%d", trace.Response.Status)
	}
	upstream := "pending"
	if trace.UpstreamResponse.Status != 0 {
		upstream = fmt.Sprintf("%d", trace.UpstreamResponse.Status)
	}
	return fmt.Sprintf("client %s | upstream %s", client, upstream)
}

func traceDuration(trace RequestTrace) string {
	if trace.Duration <= 0 {
		return "pending"
	}
	return trace.Duration.Round(time.Millisecond).String()
}

func (m dashboardModel) detailDimensions() (int, int) {
	headerHeight := lineCount(m.header())
	footerHeight := lineCount(m.footer())
	bodyHeight := max(0, m.height-headerHeight-footerHeight)
	if m.mode() == dashboardTiny {
		return max(1, m.width), bodyHeight
	}
	if m.width < 72 {
		return max(1, m.width-2), max(0, bodyHeight-2)
	}
	leftOuter := min(38, max(26, m.width/3))
	rightOuter := m.width - leftOuter - 1
	return max(1, rightOuter-2), max(0, bodyHeight-2)
}

func (m dashboardModel) maxScroll() int {
	width, height := m.detailDimensions()
	return max(0, len(m.traceDetailLines(width))-height)
}

func (m dashboardModel) maxHorizontalScroll() int {
	if !m.noWrap || len(m.traces) == 0 || m.selected < 0 || m.selected >= len(m.traces) {
		return 0
	}
	payload, _ := tracePanel(m.traces[m.selected], m.panel)
	formatted, ok := m.formattedPayload(payload.Body)
	if !ok {
		return 0
	}
	width, _ := m.detailDimensions()
	longest := 0
	for _, line := range strings.Split(formatted, "\n") {
		longest = max(longest, len([]rune(line)))
	}
	return max(0, longest-width)
}

func (m dashboardModel) pageScrollStep() int {
	_, height := m.detailDimensions()
	return max(1, height-2)
}

func (m *dashboardModel) clampScroll() {
	m.scroll = min(max(0, m.scroll), m.maxScroll())
	m.horizontal = min(max(0, m.horizontal), m.maxHorizontalScroll())
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
	text := plainStatusLabel(status)
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

func plainStatusLabel(status int) string {
	switch {
	case status == 0:
		return "[WAIT]"
	case status >= 500:
		return fmt.Sprintf("[ERR] %d", status)
	case status >= 400:
		return fmt.Sprintf("[WARN] %d", status)
	case status >= 200 && status < 300:
		return fmt.Sprintf("[OK] %d", status)
	default:
		return fmt.Sprintf("[INFO] %d", status)
	}
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
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
