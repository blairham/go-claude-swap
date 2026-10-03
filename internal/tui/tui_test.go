package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/blairham/go-claude-swap/internal/switcher"
)

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func switchModel() model {
	return model{
		page:   pageSwitch,
		snaps:  []switcher.Snapshot{{Slot: 1}, {Slot: 3}},
		cursor: 1,
	}
}

func TestRemoveConfirmY(t *testing.T) {
	next, _ := switchModel().handleKey(key("d"))
	m := next.(model)
	if m.confirmRemove != 3 {
		t.Fatalf("confirmRemove = %d, want 3 (the cursored slot)", m.confirmRemove)
	}
	next, cmd := m.handleKey(key("y"))
	m = next.(model)
	if cmd == nil || !m.busy || m.confirmRemove != 0 {
		t.Fatalf("y: cmd=%v busy=%v confirmRemove=%d, want a remove command in flight", cmd != nil, m.busy, m.confirmRemove)
	}
}

func TestRemoveConfirmOtherKeyCancels(t *testing.T) {
	for _, k := range []string{"n", "q", "d"} {
		next, _ := switchModel().handleKey(key("d"))
		next, cmd := next.(model).handleKey(key(k))
		m := next.(model)
		if cmd != nil || m.busy || m.confirmRemove != 0 {
			t.Errorf("%q: cmd=%v busy=%v confirmRemove=%d, want canceled", k, cmd != nil, m.busy, m.confirmRemove)
		}
		if m.page != pageSwitch {
			t.Errorf("%q: the canceling key also navigated (page %d)", k, m.page)
		}
	}
}

func TestRemoveRefusedWhileBusy(t *testing.T) {
	m := switchModel()
	m.busy = true
	next, _ := m.handleKey(key("d"))
	if got := next.(model).confirmRemove; got != 0 {
		t.Fatalf("confirmRemove = %d while busy, want 0", got)
	}
}
