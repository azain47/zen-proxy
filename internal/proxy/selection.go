package proxy

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var spinnerFrames = []string{"|", "/", "-", "\\"}

var wordmarkPalette = []lipgloss.Color{
	lipgloss.Color("#67E8F9"),
	lipgloss.Color("#22D3EE"),
	lipgloss.Color("#38BDF8"),
	lipgloss.Color("#818CF8"),
	lipgloss.Color("#A78BFA"),
	lipgloss.Color("#38BDF8"),
	lipgloss.Color("#22D3EE"),
}

// wordmarkStyle cycles the header wordmark hue over successive animation frames.
func (m dashboardModel) wordmarkStyle() lipgloss.Style {
	color := wordmarkPalette[(m.frame/2)%len(wordmarkPalette)]
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}

func (m dashboardModel) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

func (m dashboardModel) pulse() string {
	if m.frame%2 == 0 {
		return "*"
	}
	return "+"
}

// stripANSI removes SGR/CSI escape sequences so a rendered frame can be measured
// and sliced by display column.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) {
					c := s[i]
					i++
					if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
						break
					}
				}
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func selectionBounds(sx, sy, ex, ey int) (int, int, int, int) {
	if sy > ey {
		sy, ey = ey, sy
	}
	if sx > ex {
		sx, ex = ex, sx
	}
	return max(0, sx), max(0, sy), max(0, ex), max(0, ey)
}

// selectionText returns the plain text under the current rectangular selection.
func (m dashboardModel) selectionText() string {
	return extractSelection(m.View(), m.selStartX, m.selStartY, m.selEndX, m.selEndY)
}

func extractSelection(frame string, sx, sy, ex, ey int) string {
	x0, y0, x1, y1 := selectionBounds(sx, sy, ex, ey)
	rows := strings.Split(frame, "\n")
	var b strings.Builder
	for y := y0; y <= y1 && y < len(rows); y++ {
		runes := []rune(stripANSI(rows[y]))
		start := min(x0, len(runes))
		end := min(x1+1, len(runes))
		if end < start {
			end = start
		}
		if y > y0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(string(runes[start:end]), " "))
	}
	return strings.Trim(b.String(), "\n")
}

// applySelectionOverlay reverse-highlights the selected cells on top of the
// composed frame. Selected rows fall back to plain text so the highlight box
// renders cleanly over otherwise-styled content.
func applySelectionOverlay(frame string, sx, sy, ex, ey int) string {
	x0, y0, x1, y1 := selectionBounds(sx, sy, ex, ey)
	rows := strings.Split(frame, "\n")
	for y := y0; y <= y1 && y < len(rows); y++ {
		runes := []rune(stripANSI(rows[y]))
		for len(runes) <= x1 {
			runes = append(runes, ' ')
		}
		start := min(x0, len(runes))
		end := min(x1+1, len(runes))
		if end <= start {
			continue
		}
		rows[y] = string(runes[:start]) + selectStyle.Render(string(runes[start:end])) + string(runes[end:])
	}
	return strings.Join(rows, "\n")
}
