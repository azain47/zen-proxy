package proxy

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The model catalogue describes the proxy's configuration, not a request, so
// it is a separate full-width view toggled with "m" rather than one of the
// per-trace detail tabs.

const (
	// modelTableMinIDWidth is the narrowest model column the table layout
	// accepts; below it the view falls back to two-line rows.
	modelTableMinIDWidth = 16
	modelTableCtxWidth   = 7
	modelTableCapsWidth  = len("tools reasoning? vision")
	modelTableOwnerMin   = 6
	modelTableGap        = 2
	modelTableMarker     = 2
)

func (m *dashboardModel) setModelsView(show bool) {
	m.showModels = show
	m.scroll = 0
	m.horizontal = 0
}

// updateModelsKey handles keys while the model catalogue is open. Navigation
// keys scroll the list; panel keys return to the traffic view on that panel so
// muscle memory from the trace tabs keeps working.
func (m dashboardModel) updateModelsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "m", "esc":
		m.setModelsView(false)
	case "1", "2", "3", "4":
		m.setModelsView(false)
		m.selectPanel(int(key[0] - '1'))
	case "tab", "]", "shift+tab", "[":
		m.setModelsView(false)
	case "up", "k", "p":
		m.scroll--
	case "down", "j", "n":
		m.scroll++
	case "pgdown", "ctrl+d", "ctrl+f", " ":
		m.scroll += m.pageScrollStep()
	case "pgup", "ctrl+u", "ctrl+b":
		m.scroll -= m.pageScrollStep()
	case "home", "g":
		m.scroll = 0
	case "end", "G":
		m.scroll = m.maxScroll()
	case "c", "y", "Y":
		return m, copyMarkdownCmd(m.modelsMarkdown(), "model list as Markdown")
	}
	m.clampScroll()
	return m, nil
}

func (m dashboardModel) modelsHeader() string {
	free, paid := m.partitionModels()
	counts := fmt.Sprintf("%d models (%d %s, %d other)", len(m.config.Models), len(free), strings.ToLower(m.freeLabel()), len(paid))
	if m.mode() == dashboardTiny {
		return accentStyle.Render(fitText(fmt.Sprintf("ZEN PROXY | MODELS %d", len(m.config.Models)), m.width))
	}
	info := fmt.Sprintf(" | MODEL CATALOG | %s | %s", m.config.Provider, counts)
	if m.mode() == dashboardWide {
		info = fmt.Sprintf(" | MODEL CATALOG | %s | %s | %s", m.config.Provider, m.config.Address, counts)
	}
	return m.wordmarkStyle().Render("ZEN PROXY") + mutedStyle.Render(fitText(info, max(1, m.width-9)))
}

func (m dashboardModel) modelsView(bodyHeight int) string {
	if m.mode() == dashboardTiny {
		return m.modelsViewport(m.width, bodyHeight)
	}
	return renderDashboardPanel(m.modelsViewport(m.width-2, bodyHeight-2), m.width, bodyHeight)
}

func (m dashboardModel) modelsViewport(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	all := m.modelsViewLines(width)
	scroll := min(max(0, m.scroll), max(0, len(all)-height))
	return strings.Join(all[scroll:min(len(all), scroll+height)], "\n")
}

// modelsViewLines renders the catalogue as a table with one row per model,
// grouped into free and other models like the `models` CLI command. Narrow
// terminals get two-line rows so long ids are not squeezed to nothing.
func (m dashboardModel) modelsViewLines(width int) []string {
	current := m.config.Model
	lines := []string{
		accentStyle.Render(fitText(fmt.Sprintf("MODELS %d", len(m.config.Models)), width)),
		mutedStyle.Render(fitText(fmt.Sprintf("* default  %s", emptyAs(current, "unavailable")), width)),
		"",
	}
	if len(m.config.Models) == 0 {
		return append(lines, mutedStyle.Render(wrapText(
			"Upstream reported no models. Check the models URL or restart the proxy to re-fetch.", width)))
	}

	free, paid := m.partitionModels()
	layout, table := newModelTableLayout(m.config.Models, width)
	if table {
		lines = append(lines, mutedStyle.Render(layout.row("", "MODEL", "CONTEXT", "CAPABILITIES", "OWNER")))
	}
	groups := []struct {
		title  string
		models []ModelInfo
	}{{strings.ToUpper(m.freeLabel()), free}, {"OTHER", paid}}
	for i, group := range groups {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, accentStyle.Render(fitText(fmt.Sprintf("%s %d", group.title, len(group.models)), width)))
		if len(group.models) == 0 {
			lines = append(lines, mutedStyle.Render(fitText("  none reported", width)))
			continue
		}
		for _, model := range group.models {
			marker := ""
			if model.ID == current {
				marker = "*"
			}
			id := emptyAs(model.ID, "(unnamed)")
			if table {
				row := layout.row(marker, id, modelContext(model), strings.Join(modelCapabilities(model), " "), model.OwnedBy)
				if marker != "" {
					row = accentStyle.Render(row)
				}
				lines = append(lines, row)
				continue
			}
			first := fitText(padText(marker, modelTableMarker)+id, width)
			if marker != "" {
				first = accentStyle.Render(first)
			}
			lines = append(lines, first, mutedStyle.Render(fitText("    "+modelDetail(model), width)))
		}
	}
	return lines
}

type modelTableLayout struct {
	idWidth    int
	ownerWidth int
}

// newModelTableLayout sizes the model column to the longest id that fits and
// gives the owner column whatever is left. ok is false when the terminal is too
// narrow for a readable table.
func newModelTableLayout(models []ModelInfo, width int) (modelTableLayout, bool) {
	longest := len("MODEL")
	for _, model := range models {
		longest = max(longest, len([]rune(model.ID)))
	}
	fixed := modelTableMarker + modelTableCtxWidth + modelTableCapsWidth + 3*modelTableGap
	available := width - fixed
	idWidth := min(longest, available-modelTableOwnerMin)
	if idWidth < modelTableMinIDWidth {
		idWidth = min(longest, available)
		if idWidth < modelTableMinIDWidth {
			return modelTableLayout{}, false
		}
		return modelTableLayout{idWidth: idWidth}, true
	}
	return modelTableLayout{idWidth: idWidth, ownerWidth: available - idWidth}, true
}

func (layout modelTableLayout) row(marker, id, ctx, caps, owner string) string {
	gap := strings.Repeat(" ", modelTableGap)
	row := padText(marker, modelTableMarker) + padText(fitText(id, layout.idWidth), layout.idWidth) + gap +
		padText(fitText(ctx, modelTableCtxWidth), modelTableCtxWidth) + gap +
		padText(fitText(caps, modelTableCapsWidth), modelTableCapsWidth)
	if layout.ownerWidth > 0 && owner != "" {
		row += gap + fitText(owner, layout.ownerWidth)
	}
	return strings.TrimRight(row, " ")
}

func padText(text string, width int) string {
	if pad := width - len([]rune(text)); pad > 0 {
		return text + strings.Repeat(" ", pad)
	}
	return text
}

func (m dashboardModel) freeLabel() string {
	if m.config.Provider == providerOpenRouter {
		return "Free text"
	}
	return "Free"
}

// partitionModels splits the catalogue into free and paid groups with the same
// rule summarizeModels applies, so the view agrees with the startup summary and
// the `models` CLI command.
func (m dashboardModel) partitionModels() (free, paid []ModelInfo) {
	for _, model := range m.config.Models {
		isFree := isFreeModel(model)
		if m.config.Provider == providerOpenRouter {
			isFree = isFree && hasTextOnlyOutput(model)
		}
		if isFree {
			free = append(free, model)
		} else {
			paid = append(paid, model)
		}
	}
	return free, paid
}

func modelContext(model ModelInfo) string {
	if model.ContextLength <= 0 {
		return "-"
	}
	return formatTokenCount(model.ContextLength)
}

// modelCapabilities lists the capabilities the upstream or metadata catalog
// reports. Reasoning support that was never reported is shown as unknown.
func modelCapabilities(model ModelInfo) []string {
	if model.Capabilities == nil {
		return []string{"-"}
	}
	capabilities := model.Capabilities
	parts := []string{}
	if capabilities.SupportsFunctionCalling {
		parts = append(parts, "tools")
	}
	if !capabilities.ReasoningSupportKnown {
		parts = append(parts, "reasoning?")
	} else if capabilities.SupportsReasoning {
		parts = append(parts, "reasoning")
	}
	if capabilities.SupportsVision {
		parts = append(parts, "vision")
	}
	if len(parts) == 0 {
		return []string{"-"}
	}
	return parts
}

// modelDetail summarises what is known about a model beyond its id. Only
// fields the upstream actually reports are printed.
func modelDetail(model ModelInfo) string {
	parts := []string{}
	if model.ContextLength > 0 {
		parts = append(parts, fmt.Sprintf("%s ctx", formatTokenCount(model.ContextLength)))
	}
	if model.Capabilities != nil {
		for _, capability := range modelCapabilities(model) {
			if capability != "-" {
				parts = append(parts, capability)
			}
		}
	}
	if model.Pricing != nil &&
		priceIsZero(model.Pricing.Prompt) && priceIsZero(model.Pricing.Completion) {
		parts = append(parts, "$0")
	}
	if model.OwnedBy != "" {
		parts = append(parts, model.OwnedBy)
	}
	return emptyAs(strings.Join(parts, " | "), "no metadata")
}

// formatTokenCount abbreviates a token count the way the upstream models list
// does (200000 -> "200k", 262144 -> "262k", 1050000 -> "1.05M"), keeping it
// short enough for the context column.
func formatTokenCount(count int) string {
	switch {
	case count >= 1_000_000:
		return trimDecimal(float64(count)/1_000_000, 2) + "M"
	case count >= 10_000:
		return trimDecimal(float64(count)/1_000, 0) + "k"
	case count >= 1_000:
		return trimDecimal(float64(count)/1_000, 1) + "k"
	default:
		return strconv.Itoa(count)
	}
}

func trimDecimal(value float64, precision int) string {
	text := strconv.FormatFloat(value, 'f', precision, 64)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return text
}
