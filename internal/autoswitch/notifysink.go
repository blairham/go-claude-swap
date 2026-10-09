package autoswitch

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/notify"
)

// Notification levels (the autoswitch.notify setting).
const (
	NotifyOff       = "off"
	NotifyImportant = "important" // at-limit, failover, all exhausted, recovery
	NotifyAll       = "all"       // important plus every switch and quarantine
)

// NotifySink forwards every event to next unchanged and posts a desktop
// notification for the moments a user would otherwise notice by being
// stopped: an at-limit or failover switch, every account exhausted, and the
// recovery from that. "all exhausted" is announced once per episode, not on
// every blocked tick, and recovery is announced when a switch lands or when
// an account that was exhausted shows headroom again.
//
// It must wrap anything that filters events (QuietSink), because recovery
// is read from polls a quiet log would drop.
type NotifySink struct {
	next     EventSink
	level    string
	notifier notify.Notifier

	mu        sync.Mutex
	lastHeads map[string]*float64 // headroom from the most recent poll
	exhausted map[string]bool     // non-nil while an all-exhausted episode is open
}

// NewNotifySink wraps next. An unknown level behaves as NotifyOff.
func NewNotifySink(next EventSink, level string, n notify.Notifier) *NotifySink {
	if n == nil {
		n = notify.Nop
	}
	return &NotifySink{next: next, level: level, notifier: n}
}

// Emit implements EventSink.
func (s *NotifySink) Emit(ev Event) {
	if s.next != nil {
		s.next.Emit(ev)
	}
	if s.level != NotifyImportant && s.level != NotifyAll {
		return
	}
	s.mu.Lock()
	title, body := s.observe(ev)
	s.mu.Unlock()
	if title != "" {
		// Failure to notify is never the loop's problem; the spawn is
		// already bounded by notify.Timeout.
		_ = s.notifier.Notify(title, body)
	}
}

// observe updates the episode state and returns the notification ev calls
// for, if any.
func (s *NotifySink) observe(ev Event) (title, body string) {
	switch ev.Kind {
	case "poll":
		heads, _ := ev.Fields["headroomPct"].(map[string]*float64)
		s.lastHeads = heads
		if s.exhausted == nil {
			return "", ""
		}
		var back []string
		for slot := range s.exhausted {
			if h := heads[slot]; h != nil && *h > 0 {
				back = append(back, slot)
			}
		}
		if len(back) == 0 {
			return "", ""
		}
		sort.Strings(back)
		s.exhausted = nil
		return "cswap: accounts available again", "Account-" + back[0] + " has headroom again"

	case "all-exhausted":
		if s.exhausted != nil {
			return "", "" // same episode, already announced
		}
		s.exhausted = map[string]bool{}
		for slot, h := range s.lastHeads {
			if h != nil && *h <= 0 {
				s.exhausted[slot] = true
			}
		}
		body = "No recovery time known"
		if t, err := time.Parse(account.TimeFormat, ev.str("earliestResetAt")); err == nil {
			body = "Earliest recovery " + t.Local().Format("Mon 15:04")
		}
		return "cswap: all accounts exhausted", body

	case "switch":
		if dry, _ := ev.Fields["dryRun"].(bool); dry {
			return "", ""
		}
		from, to := ev.num("from"), ev.num("to")
		target := fmt.Sprintf("Account-%d (%s)", to, ev.str("toEmail"))
		if s.exhausted != nil {
			s.exhausted = nil
			return "cswap: accounts available again", "Switched to " + target
		}
		switch trigger := ev.str("trigger"); trigger {
		case triggerAtLimit:
			return "cswap: switched account", fmt.Sprintf("Account-%d hit its limit — now on %s", from, target)
		case triggerFailover:
			return "cswap: switched account", fmt.Sprintf("Account-%d usage unknown — failed over to %s", from, target)
		default:
			if s.level != NotifyAll {
				return "", ""
			}
			return "cswap: switched account", fmt.Sprintf("Account-%d -> %s (%s)", from, target, trigger)
		}

	case "account-quarantined":
		if s.level != NotifyAll {
			return "", ""
		}
		return "cswap: account quarantined", fmt.Sprintf("Account-%s (%s): %s",
			strconv.Itoa(ev.num("number")), ev.str("email"), ev.str("reason"))
	}
	return "", ""
}
