// Package tui implements cswap's full-screen terminal interface: a
// dashboard of account usage, a switch picker, and a live watch view,
// built on Bubble Tea.
package tui

import (
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/settings"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

// Poll and display cadences.
const (
	pollEvery    = 3 * time.Second
	frameEvery   = time.Second
	toastFor     = 3 * time.Second
	staleStatusS = time.Minute
)

// page identifies which screen is showing.
type page int

// Screens.
const (
	pageDashboard page = iota
	pageSwitch
	pageWatch
)

// Menu entries on the dashboard.
var menuItems = []string{"Switch account…", "Watch accounts", "Quit"}

// snapshotMsg carries a completed usage collection.
type snapshotMsg struct {
	snaps []switcher.Snapshot
	err   string
	at    time.Time
}

// pollTickMsg fires the 3s snapshot cadence.
type pollTickMsg time.Time

// frameTickMsg fires the 1s re-render cadence for countdowns.
type frameTickMsg time.Time

// switchDoneMsg carries a completed switch attempt.
type switchDoneMsg struct {
	res *switcher.Result
	err error
}

// model is the whole TUI state.
type model struct {
	page   page
	width  int
	height int

	themeName string
	pal       palette
	threshold float64
	models    []string

	snaps    []switcher.Snapshot
	loadErr  string
	lastSnap time.Time

	collecting bool // a Collect is in flight
	busy       bool // a mutating action (switch) is in flight

	cursor  int  // card cursor on switch/watch screens
	armed   bool // watch: selection mode engaged
	menuIdx int  // dashboard menu cursor

	toast   string
	toastAt time.Time
	now     time.Time
}

// Run starts the full-screen TUI on the given page: "dashboard" or "watch".
func Run(pageName string) error {
	s := settings.Load()
	m := model{
		themeName: s.String("ui.theme"),
		threshold: s.Float("autoswitch.threshold"),
		models:    s.Models(),
		now:       time.Now(),
	}
	m.pal = paletteFor(m.themeName)
	if pageName == "watch" {
		m.page = pageWatch
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// Init kicks off the first collection and both tickers.
func (m model) Init() tea.Cmd {
	return tea.Batch(collectCmd(false, m.models), frameTick(), pollTick())
}

// collectCmd gathers snapshots off the UI goroutine. storeOnly avoids
// network fetches (used while a mutating action holds the lane).
func collectCmd(storeOnly bool, models []string) tea.Cmd {
	return func() tea.Msg {
		seq, err := account.Load()
		if err != nil {
			return snapshotMsg{err: err.Error(), at: time.Now()}
		}
		c := &switcher.Collector{StoreOnly: storeOnly, Models: models}
		return snapshotMsg{snaps: c.Collect(seq), at: time.Now()}
	}
}

// switchCmd runs a switch off the UI goroutine.
func switchCmd(slot int) tea.Cmd {
	return func() tea.Msg {
		res, err := switcher.SwitchTo(strconv.Itoa(slot), false)
		return switchDoneMsg{res: res, err: err}
	}
}

func frameTick() tea.Cmd {
	return tea.Tick(frameEvery, func(t time.Time) tea.Msg { return frameTickMsg(t) })
}

func pollTick() tea.Cmd {
	return tea.Tick(pollEvery, func(t time.Time) tea.Msg { return pollTickMsg(t) })
}

// Update is the single message loop.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case frameTickMsg:
		m.now = time.Time(msg)
		if m.toast != "" && m.now.Sub(m.toastAt) > toastFor {
			m.toast = ""
		}
		return m, frameTick()

	case pollTickMsg:
		cmds := []tea.Cmd{pollTick()}
		if !m.collecting {
			m.collecting = true
			cmds = append(cmds, collectCmd(m.busy, m.models))
		}
		return m, tea.Batch(cmds...)

	case snapshotMsg:
		m.collecting = false
		m.lastSnap = msg.at
		if msg.err != "" {
			m.loadErr = msg.err
			return m, nil
		}
		m.loadErr = ""
		m.snaps = msg.snaps
		if m.cursor >= len(m.snaps) {
			m.cursor = max(len(m.snaps)-1, 0)
		}
		return m, nil

	case switchDoneMsg:
		return m.finishSwitch(msg)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// finishSwitch clears the busy flag, toasts the outcome, and refreshes.
func (m model) finishSwitch(msg switchDoneMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	switch {
	case msg.err != nil:
		m.setToast("Switch failed: " + msg.err.Error())
	case msg.res != nil && msg.res.Switched:
		toast := "Switched to " + msg.res.ToEmail
		if len(msg.res.Warnings) > 0 {
			toast += " — " + msg.res.Warnings[0]
		}
		m.setToast(toast)
	case msg.res != nil:
		m.setToast("Not switched: " + msg.res.Reason)
	}
	if m.page == pageSwitch {
		m.page = pageDashboard
	}
	var cmd tea.Cmd
	if !m.collecting {
		m.collecting = true
		cmd = collectCmd(false, m.models)
	}
	return m, cmd
}

// handleKey routes keys: global bindings first, then the active screen's.
func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "ctrl+t":
		return m.cycleTheme(), nil
	case "f":
		var cmd tea.Cmd
		if !m.collecting {
			m.collecting = true
			cmd = collectCmd(m.busy, m.models)
		}
		m.setToast("Refreshing usage…")
		return m, cmd
	}
	switch m.page {
	case pageSwitch:
		return m.handleSwitchKey(msg)
	case pageWatch:
		return m.handleWatchKey(msg)
	default:
		return m.handleDashboardKey(msg)
	}
}

func (m model) handleDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "s":
		return m.enterSwitch(), nil
	case "w":
		m.page = pageWatch
		m.armed = false
		return m, nil
	case "j", "down":
		m.menuIdx = (m.menuIdx + 1) % len(menuItems)
	case "k", "up":
		m.menuIdx = (m.menuIdx + len(menuItems) - 1) % len(menuItems)
	case "enter":
		switch m.menuIdx {
		case 0:
			return m.enterSwitch(), nil
		case 1:
			m.page = pageWatch
			m.armed = false
			return m, nil
		default:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m model) handleSwitchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.page = pageDashboard
		return m, nil
	case "j", "down":
		m.cursor = m.moveCursor(1)
	case "k", "up":
		m.cursor = m.moveCursor(-1)
	case "enter":
		return m.startSwitch(false)
	}
	return m, nil
}

func (m model) handleWatchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "s":
		if !m.armed {
			m.armed = true
			m.cursor = m.activeIndex()
		}
		return m, nil
	case "esc":
		if m.armed {
			m.armed = false
			return m, nil
		}
		m.page = pageDashboard
		return m, nil
	case "q":
		m.page = pageDashboard
		m.armed = false
		return m, nil
	case "j", "down":
		if m.armed {
			m.cursor = m.moveCursor(1)
		}
	case "k", "up":
		if m.armed {
			m.cursor = m.moveCursor(-1)
		}
	case "enter":
		if m.armed {
			return m.startSwitch(true)
		}
	}
	return m, nil
}

// enterSwitch opens the switch screen with the cursor on the active row.
func (m model) enterSwitch() model {
	m.page = pageSwitch
	m.cursor = m.activeIndex()
	return m
}

// startSwitch launches the switch for the cursored account; single-flight.
// stayWatching keeps the watch screen (and disarms) instead of popping back.
func (m model) startSwitch(stayWatching bool) (tea.Model, tea.Cmd) {
	if m.busy {
		m.setToast("Another action is still running")
		return m, nil
	}
	if len(m.snaps) == 0 {
		return m, nil
	}
	slot := m.snaps[m.cursor].Slot
	m.busy = true
	if stayWatching {
		m.armed = false
	}
	return m, switchCmd(slot)
}

// cycleTheme advances dark → light → auto, persists best-effort, toasts.
func (m model) cycleTheme() model {
	m.themeName = nextTheme(m.themeName)
	m.pal = paletteFor(m.themeName)
	toast := "Theme: " + m.themeName
	if err := settings.SetKey("ui.theme", m.themeName); err != nil {
		toast += " (not saved)"
	}
	m.setToast(toast)
	return m
}

// moveCursor steps the card cursor, clamped to the roster.
func (m model) moveCursor(delta int) int {
	if len(m.snaps) == 0 {
		return 0
	}
	return clampInt(m.cursor+delta, 0, len(m.snaps)-1)
}

// activeIndex is the snaps index of the active account (0 when none).
func (m model) activeIndex() int {
	for i := range m.snaps {
		if m.snaps[i].Active {
			return i
		}
	}
	return 0
}

// setToast shows a transient footer message.
func (m *model) setToast(s string) {
	m.toast = s
	m.toastAt = time.Now()
}
