package tui

import "github.com/charmbracelet/lipgloss"

// palette is the color set one theme renders with.
type palette struct {
	Accent  lipgloss.Color
	Fg      lipgloss.Color
	Muted   lipgloss.Color
	Surface lipgloss.Color
	OK      lipgloss.Color
	Warn    lipgloss.Color
	Crit    lipgloss.Color
	Track   lipgloss.Color
}

var darkPalette = palette{
	Accent:  lipgloss.Color("#d7875f"),
	Fg:      lipgloss.Color("#e8e4de"),
	Muted:   lipgloss.Color("#8a8a8a"),
	Surface: lipgloss.Color("#1e1e1e"),
	OK:      lipgloss.Color("#87af87"),
	Warn:    lipgloss.Color("#d7af5f"),
	Crit:    lipgloss.Color("#d75f5f"),
	Track:   lipgloss.Color("#3a3a3a"),
}

var lightPalette = palette{
	Accent:  lipgloss.Color("#954c2a"),
	Fg:      lipgloss.Color("#2b2723"),
	Muted:   lipgloss.Color("#635d55"),
	Surface: lipgloss.Color("#efeae1"),
	OK:      lipgloss.Color("#3d6b3d"),
	Warn:    lipgloss.Color("#795911"),
	Crit:    lipgloss.Color("#ad3128"),
	Track:   lipgloss.Color("#cec7ba"),
}

// paletteFor maps a ui.theme value to a palette; "auto" (or anything
// unrecognized) assumes a dark terminal.
func paletteFor(theme string) palette {
	if theme == "light" {
		return lightPalette
	}
	return darkPalette
}

// nextTheme cycles dark → light → auto → dark.
func nextTheme(cur string) string {
	switch cur {
	case "dark":
		return "light"
	case "light":
		return "auto"
	default:
		return "dark"
	}
}

// severity picks the fill color for a usage percentage: ≥90 critical,
// ≥70 warning, otherwise healthy.
func (p palette) severity(pct float64) lipgloss.Color {
	switch {
	case pct >= 90:
		return p.Crit
	case pct >= 70:
		return p.Warn
	default:
		return p.OK
	}
}
