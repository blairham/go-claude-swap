package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/blairham/go-claude-swap/internal/switcher"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// Card layout constants.
const (
	bodyIndent   = 4   // spaces before each bar row
	barChromeW   = 42  // reserved for indent, pct, and the resets suffix
	barMinW      = 12  // narrowest usable bar
	barMaxW      = 30  // widest bar
	dimAfterS    = 300 // seconds of staleness before a card dims
	ageNoteAfter = 180 // seconds of staleness before the header shows an age
)

// barRow is one renderable usage line inside a full card.
type barRow struct {
	label    string
	pct      float64
	resetsAt string
	spend    *usage.Spend // non-nil marks the spend row
	weekly   bool         // 7d and scoped model windows
}

// buildRows assembles bar rows in display order: spend, 5h, 7d, then each
// scoped model window.
func buildRows(u *usage.Usage) []barRow {
	var rows []barRow
	if u.Spend != nil {
		rows = append(rows, barRow{label: "$$", pct: u.Spend.Pct, spend: u.Spend})
	}
	if u.FiveHour != nil {
		rows = append(rows, barRow{label: "5h", pct: u.FiveHour.Pct, resetsAt: u.FiveHour.ResetsAt})
	}
	if u.SevenDay != nil {
		rows = append(rows, barRow{label: "7d", pct: u.SevenDay.Pct, resetsAt: u.SevenDay.ResetsAt, weekly: true})
	}
	for _, s := range u.Scoped {
		rows = append(rows, barRow{label: s.Name, pct: s.Pct, resetsAt: s.ResetsAt, weekly: true})
	}
	return rows
}

// renderCard renders one account's full card: a header line plus a bar row
// per usage window (or a sentinel line when usage is unavailable).
func renderCard(pal palette, snap *switcher.Snapshot, width int, threshold float64, now time.Time) string {
	lines := []string{cardHeader(pal, snap, now)}
	if snap.Status == switcher.StatusOK {
		lines = append(lines, cardBars(pal, snap, width, threshold, now)...)
	} else {
		lines = append(lines, sentinelLines(pal, snap)...)
	}
	return strings.Join(lines, "\n")
}

// cardHeader renders `{slot}  alias (email)  [org]  ● active  (disabled)  · age`.
func cardHeader(pal palette, snap *switcher.Snapshot, now time.Time) string {
	a := snap.Account
	fg := lipgloss.NewStyle().Foreground(pal.Fg)
	muted := lipgloss.NewStyle().Foreground(pal.Muted)
	accent := lipgloss.NewStyle().Foreground(pal.Accent).Bold(true)

	name := fg.Render(a.Email)
	if a.Alias != "" {
		name = accent.Render(a.Alias) + muted.Render(" ("+a.Email+")")
	}
	org := a.OrganizationName
	if org == "" {
		org = "personal"
	}

	var b strings.Builder
	b.WriteString(muted.Render(fmt.Sprintf("%2d", snap.Slot)))
	b.WriteString("  " + name)
	b.WriteString("  " + muted.Render("["+org+"]"))
	if snap.Active {
		b.WriteString("   " + accent.Render("● active"))
	}
	if a.Disabled {
		b.WriteString("   " + muted.Render("(disabled)"))
	}
	if !math.IsInf(snap.Age, 1) && snap.Age > ageNoteAfter {
		b.WriteString("   " + muted.Render("· "+ageString(snap.Age)+" ago"))
	}
	return b.String()
}

// cardBars renders one bar row per usage window.
func cardBars(pal palette, snap *switcher.Snapshot, width int, threshold float64, now time.Time) []string {
	u := snap.Usage
	if u == nil {
		u = snap.LastGood
	}
	if u == nil {
		return []string{strings.Repeat(" ", bodyIndent) + lipgloss.NewStyle().Foreground(pal.Muted).Render("usage unavailable")}
	}
	rows := buildRows(u)
	labelW := 2
	for _, r := range rows {
		if len(r.label) > labelW {
			labelW = len(r.label)
		}
	}
	barW := clampInt(width-barChromeW-labelW, barMinW, barMaxW)
	dim := snap.Age > dimAfterS

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, renderBarRow(pal, r, labelW, barW, width, threshold, dim, now))
	}
	return out
}

// renderBarRow renders `label ━━━╸──┃──  47%  resets 2h 13m · 20:39`.
func renderBarRow(pal palette, r barRow, labelW, barW, panelW int, threshold float64, dim bool, now time.Time) string {
	muted := lipgloss.NewStyle().Foreground(pal.Muted)
	pctColor := pal.severity(r.pct)
	if dim {
		pctColor = pal.Muted
	}
	pctStyle := lipgloss.NewStyle().Foreground(pctColor)

	pctStr := fmt.Sprintf("%3.0f%%", math.Min(math.Max(r.pct, 0), 999))
	bang := ""
	if r.weekly && r.pct >= 100 {
		bang = " (!)"
	}
	suffix := r.suffix(now)

	plainLen := bodyIndent + labelW + 1 + barW + 2 + len(pctStr) + len(bang)
	if suffix != "" && plainLen+2+len(suffix) > panelW {
		suffix = "" // row would overflow: drop the resets/spend detail
	}

	var b strings.Builder
	b.WriteString(strings.Repeat(" ", bodyIndent))
	b.WriteString(muted.Render(fmt.Sprintf("%-*s", labelW, r.label)))
	b.WriteString(" ")
	b.WriteString(renderBar(pal, r.pct, threshold, barW, dim))
	b.WriteString("  " + pctStyle.Render(pctStr))
	if bang != "" {
		bangColor := pal.Crit
		if dim {
			bangColor = pal.Muted
		}
		b.WriteString(lipgloss.NewStyle().Foreground(bangColor).Render(bang))
	}
	if suffix != "" {
		b.WriteString("  " + muted.Render(suffix))
	}
	return b.String()
}

// suffix is the trailing detail: spend amounts, or the reset countdown+clock.
func (r barRow) suffix(now time.Time) string {
	if r.spend != nil {
		if r.spend.Limit > 0 {
			return fmt.Sprintf("$%.2f / $%.2f", r.spend.Used, r.spend.Limit)
		}
		return fmt.Sprintf("$%.2f", r.spend.Used)
	}
	ts := usage.ParseReset(r.resetsAt)
	if ts == 0 {
		return ""
	}
	countdown := usage.FormatCountdown(time.Unix(ts, 0).Sub(now))
	return "resets " + countdown + " · " + usage.FormatClock(ts, now)
}

// renderBar draws the bar track: full ━, a half ╸ for the fractional cell,
// empty ─, and a warn-colored threshold tick ┃ over the empty region.
func renderBar(pal palette, pct, threshold float64, width int, dim bool) string {
	frac := math.Min(math.Max(pct, 0), 100) / 100
	cells := frac * float64(width)
	full := int(cells)
	half := cells-float64(full) > 0 && full < width
	tick := clampInt(int(math.Round(threshold/100*float64(width))), 0, width-1)

	fillColor, trackColor, tickColor := pal.severity(pct), pal.Track, pal.Warn
	if dim {
		fillColor, trackColor, tickColor = pal.Muted, pal.Muted, pal.Muted
	}
	fillSt := lipgloss.NewStyle().Foreground(fillColor)
	trackSt := lipgloss.NewStyle().Foreground(trackColor)
	tickSt := lipgloss.NewStyle().Foreground(tickColor)

	var b strings.Builder
	for i := range width {
		switch {
		case i < full:
			b.WriteString(fillSt.Render("━"))
		case i == full && half:
			b.WriteString(fillSt.Render("╸"))
		case i == tick:
			b.WriteString(tickSt.Render("┃"))
		default:
			b.WriteString(trackSt.Render("─"))
		}
	}
	return b.String()
}

// sentinelLines renders the no-bars body for a non-OK status, plus a
// last-seen line when stale data exists.
func sentinelLines(pal palette, snap *switcher.Snapshot) []string {
	muted := lipgloss.NewStyle().Foreground(pal.Muted)
	warn := lipgloss.NewStyle().Foreground(pal.Warn)
	indent := strings.Repeat(" ", bodyIndent)

	var line string
	switch snap.Status {
	case switcher.StatusTokenExpired:
		line = warn.Render("⚠ token expired — refresh deferred; retries automatically")
	case switcher.StatusAPIKey:
		line = muted.Render("· API key (no quota)")
	case switcher.StatusKeychainUnavailable:
		line = warn.Render("⚠ keychain unavailable")
	case switcher.StatusReloginRequired:
		line = warn.Render("⚠ re-login needed")
	case switcher.StatusNoCredentials:
		line = warn.Render("⚠ no stored credentials")
	default:
		msg := "usage unavailable"
		if snap.LastErr != "" {
			msg += " — " + snap.LastErr
		}
		line = muted.Render(msg)
	}
	out := []string{indent + line}
	if snap.LastGood != nil {
		seen := fmt.Sprintf("└ last seen %.0f%% used · %s ago", maxWindowPct(snap.LastGood), ageString(snap.Age))
		out = append(out, indent+muted.Render(seen))
	}
	return out
}

// renderMini renders a one-line summary for an inactive account:
// `2  work@acme.dev [personal]   5h 92% · 7d 63%`.
func renderMini(pal palette, snap *switcher.Snapshot) string {
	a := snap.Account
	fg := lipgloss.NewStyle().Foreground(pal.Fg)
	muted := lipgloss.NewStyle().Foreground(pal.Muted)

	org := a.OrganizationName
	if org == "" {
		org = "personal"
	}
	var b strings.Builder
	b.WriteString(muted.Render(fmt.Sprintf("%2d", snap.Slot)))
	b.WriteString("  " + fg.Render(a.Email))
	b.WriteString(" " + muted.Render("["+org+"]"))
	if a.Disabled {
		b.WriteString(" " + muted.Render("(disabled)"))
	}
	b.WriteString("   " + miniSummary(pal, snap))
	return b.String()
}

// miniSummary is the usage tail of a mini row: severity-colored 5h/7d
// percentages, or a short sentinel label.
func miniSummary(pal palette, snap *switcher.Snapshot) string {
	muted := lipgloss.NewStyle().Foreground(pal.Muted)
	warn := lipgloss.NewStyle().Foreground(pal.Warn)

	switch snap.Status {
	case switcher.StatusOK:
		// Percentages below.
	case switcher.StatusTokenExpired:
		return warn.Render("token expired")
	case switcher.StatusAPIKey:
		return muted.Render("API key")
	case switcher.StatusKeychainUnavailable:
		return warn.Render("keychain unavailable")
	case switcher.StatusReloginRequired:
		return warn.Render("re-login needed")
	case switcher.StatusNoCredentials:
		return warn.Render("no credentials")
	default:
		return muted.Render("usage unknown")
	}

	u := snap.Usage
	if u == nil {
		u = snap.LastGood
	}
	if u == nil {
		return muted.Render("usage unknown")
	}
	var parts []string
	if u.FiveHour != nil {
		st := lipgloss.NewStyle().Foreground(pal.severity(u.FiveHour.Pct))
		parts = append(parts, muted.Render("5h ")+st.Render(fmt.Sprintf("%.0f%%", u.FiveHour.Pct)))
	}
	if u.SevenDay != nil {
		st := lipgloss.NewStyle().Foreground(pal.severity(u.SevenDay.Pct))
		parts = append(parts, muted.Render("7d ")+st.Render(fmt.Sprintf("%.0f%%", u.SevenDay.Pct)))
	}
	if len(parts) == 0 {
		return muted.Render("usage unknown")
	}
	return strings.Join(parts, muted.Render(" · "))
}

// maxWindowPct is the highest percentage across all windows of a snapshot.
func maxWindowPct(u *usage.Usage) float64 {
	pct := 0.0
	if u.FiveHour != nil {
		pct = math.Max(pct, u.FiveHour.Pct)
	}
	if u.SevenDay != nil {
		pct = math.Max(pct, u.SevenDay.Pct)
	}
	for _, w := range u.Scoped {
		pct = math.Max(pct, w.Pct)
	}
	return pct
}

// ageString renders a staleness age in seconds as "2m" / "never".
func ageString(age float64) string {
	if math.IsInf(age, 1) {
		return "never"
	}
	return usage.FormatCountdown(time.Duration(age * float64(time.Second)))
}

// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
