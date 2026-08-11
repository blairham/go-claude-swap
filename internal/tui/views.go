package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/blairham/go-claude-swap/internal/usage"
)

// View renders the active screen.
func (m model) View() string {
	switch m.page {
	case pageSwitch:
		return m.switchView()
	case pageWatch:
		return m.watchView()
	default:
		return m.dashboardView()
	}
}

// panelWidth is the usable content width for cards.
func (m model) panelWidth() int {
	w := m.width
	if w <= 0 {
		w = 80
	}
	return max(w-2, 40)
}

// dashboardView: active account's full card, mini rows for the rest, and
// the action menu.
func (m model) dashboardView() string {
	pal := m.pal
	var b strings.Builder

	if len(m.snaps) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(pal.Muted).
			Render("no accounts yet — run 'cswap add' to capture your Claude Code login"))
		b.WriteString("\n")
	} else {
		activeIdx := -1
		for i := range m.snaps {
			if m.snaps[i].Active {
				activeIdx = i
				break
			}
		}
		if activeIdx >= 0 {
			b.WriteString(renderCard(pal, &m.snaps[activeIdx], m.panelWidth(), m.threshold, m.now))
			b.WriteString("\n\n")
		}
		for i := range m.snaps {
			if i == activeIdx {
				continue
			}
			b.WriteString(renderMini(pal, &m.snaps[i]))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(m.renderMenu())
	b.WriteString("\n")
	b.WriteString(m.footer("s switch · w watch · f refresh · ctrl+t theme · q quit"))
	return b.String()
}

// renderMenu renders the dashboard action list.
func (m model) renderMenu() string {
	fg := lipgloss.NewStyle().Foreground(m.pal.Fg)
	accent := lipgloss.NewStyle().Foreground(m.pal.Accent).Bold(true)
	var b strings.Builder
	for i, item := range menuItems {
		if i == m.menuIdx {
			b.WriteString(accent.Render("▸ " + item))
		} else {
			b.WriteString("  " + fg.Render(item))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// switchView: full cards for every account with a selection cursor.
func (m model) switchView() string {
	title := lipgloss.NewStyle().Foreground(m.pal.Fg).Bold(true).Render("switch to which account?")
	body := m.cardList(true)
	foot := m.footer("enter switch · j/k move · esc back")
	return title + "\n\n" + body + "\n" + foot
}

// watchView: read-only full cards; s arms selection mode.
func (m model) watchView() string {
	titleText := "watching all accounts"
	if m.armed {
		titleText = "switch to which account? · enter confirm · esc cancel"
	} else if st := m.statusString(); st != "" {
		titleText += " · " + st
	}
	title := lipgloss.NewStyle().Foreground(m.pal.Fg).Bold(true).Render(titleText)
	body := m.cardList(m.armed)
	hints := "s switch · f refresh · ctrl+t theme · q back"
	if m.armed {
		hints = "enter confirm · j/k move · esc cancel"
	}
	return title + "\n\n" + body + "\n" + m.footer(hints)
}

// cardList renders every account's full card, optionally with a cursor
// gutter (accent left border on the selected card).
func (m model) cardList(withCursor bool) string {
	if len(m.snaps) == 0 {
		return lipgloss.NewStyle().Foreground(m.pal.Muted).Render("no accounts") + "\n"
	}
	accent := lipgloss.NewStyle().Foreground(m.pal.Accent)
	width := m.panelWidth() - 2 // gutter column
	var b strings.Builder
	for i := range m.snaps {
		card := renderCard(m.pal, &m.snaps[i], width, m.threshold, m.now)
		for _, line := range strings.Split(card, "\n") {
			if withCursor && i == m.cursor {
				b.WriteString(accent.Render("▍ ") + line)
			} else {
				b.WriteString("  " + line)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// statusString joins transient status notes: snapshot staleness, load errors.
func (m model) statusString() string {
	var parts []string
	if m.loadErr != "" {
		parts = append(parts, m.loadErr)
	}
	if !m.lastSnap.IsZero() {
		if age := m.now.Sub(m.lastSnap); age >= staleStatusS {
			parts = append(parts, "snapshot "+usage.FormatCountdown(age)+" ago")
		}
	}
	return strings.Join(parts, " · ")
}

// footer renders the toast line (when one is showing) above the muted
// hint/status line.
func (m model) footer(hints string) string {
	muted := lipgloss.NewStyle().Foreground(m.pal.Muted)
	parts := []string{hints}
	if st := m.statusString(); st != "" && m.page != pageWatch {
		parts = append(parts, st)
	}
	line := muted.Render(strings.Join(parts, " · "))
	if m.toast != "" {
		toast := lipgloss.NewStyle().Foreground(m.pal.Accent).Render(m.toast)
		return toast + "\n" + line
	}
	return line
}
