// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package autoswitch

import (
	"strings"
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/notify"
)

type sent struct{ title, body string }

// recorder is the stub notifier: nothing reaches a real desktop.
func recorder() (*[]sent, notify.Notifier) {
	var got []sent
	return &got, notify.Func(func(title, body string) error {
		got = append(got, sent{title, body})
		return nil
	})
}

type countSink struct{ n int }

func (c *countSink) Emit(Event) { c.n++ }

func pollEv(heads map[string]*float64) Event {
	return Event{Kind: "poll", TS: time.Now(), Fields: map[string]any{"headroomPct": heads}}
}

func switchEv(trigger string, dry bool) Event {
	return Event{Kind: "switch", TS: time.Now(), Fields: map[string]any{
		"trigger": trigger, "from": 1, "to": 2, "toEmail": "b@b.co", "dryRun": dry,
	}}
}

func exhaustedEv() Event {
	return Event{Kind: "all-exhausted", TS: time.Now(), Fields: map[string]any{"earliestResetAt": "2026-10-09T15:00:00Z"}}
}

func pct(v float64) *float64 { return &v }

func TestNotifySinkImportantSwitches(t *testing.T) {
	got, n := recorder()
	next := &countSink{}
	s := NewNotifySink(next, NotifyImportant, n)

	s.Emit(switchEv(triggerProactive, false))
	s.Emit(switchEv(triggerAtLimit, true)) // dry-run never notifies
	if len(*got) != 0 {
		t.Fatalf("important level notified %v", *got)
	}
	s.Emit(switchEv(triggerAtLimit, false))
	s.Emit(switchEv(triggerFailover, false))
	if len(*got) != 2 || !strings.Contains((*got)[0].body, "hit its limit") ||
		!strings.Contains((*got)[1].body, "failed over to Account-2 (b@b.co)") {
		t.Fatalf("notifications = %v", *got)
	}
	if next.n != 4 {
		t.Errorf("forwarded %d of 4 events", next.n)
	}
}

func TestNotifySinkExhaustedOncePerEpisodeAndRecovery(t *testing.T) {
	got, n := recorder()
	s := NewNotifySink(nil, NotifyImportant, n)

	// Active (1) near the limit, candidate (2) exhausted.
	for range 3 {
		s.Emit(pollEv(map[string]*float64{"1": pct(0), "2": pct(0)}))
		s.Emit(exhaustedEv())
	}
	if len(*got) != 1 || !strings.Contains((*got)[0].title, "all accounts exhausted") ||
		!strings.Contains((*got)[0].body, "Earliest recovery") {
		t.Fatalf("exhausted notifications = %v", *got)
	}
	// An unknown reading is not a recovery.
	s.Emit(pollEv(map[string]*float64{"1": nil, "2": pct(0)}))
	if len(*got) != 1 {
		t.Fatalf("unknown headroom read as recovery: %v", *got)
	}
	s.Emit(pollEv(map[string]*float64{"1": pct(0), "2": pct(100)}))
	if len(*got) != 2 || !strings.Contains((*got)[1].body, "Account-2 has headroom again") {
		t.Fatalf("recovery = %v", *got)
	}
	// A new episode is announced again.
	s.Emit(pollEv(map[string]*float64{"1": pct(0), "2": pct(0)}))
	s.Emit(exhaustedEv())
	if len(*got) != 3 {
		t.Fatalf("second episode = %v", *got)
	}
	// A landed switch ends the episode too.
	s.Emit(switchEv(triggerAtLimit, false))
	if len(*got) != 4 || !strings.Contains((*got)[3].title, "available again") {
		t.Fatalf("switch recovery = %v", *got)
	}
}

func TestNotifySinkLevels(t *testing.T) {
	for _, level := range []string{NotifyOff, "", "bogus"} {
		got, n := recorder()
		s := NewNotifySink(nil, level, n)
		s.Emit(switchEv(triggerAtLimit, false))
		s.Emit(pollEv(map[string]*float64{"1": pct(0)}))
		s.Emit(exhaustedEv())
		if len(*got) != 0 {
			t.Errorf("level %q notified %v", level, *got)
		}
	}
	got, n := recorder()
	s := NewNotifySink(nil, NotifyAll, n)
	s.Emit(switchEv(triggerProactive, false))
	s.Emit(
		Event{Kind: "account-quarantined", Fields: map[string]any{"number": 3, "email": "c@b.co", "reason": "invalid_grant"}},
	)
	if len(*got) != 2 || !strings.Contains((*got)[0].body, "(proactive)") ||
		!strings.Contains((*got)[1].body, "Account-3") {
		t.Fatalf("all level = %v", *got)
	}
}
