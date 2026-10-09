package autoswitch

import (
	"math"
	"strconv"
	"sync"
	"time"
)

// DefaultHeartbeat is how often QuietSink lets an unchanged poll through, so
// a quiet log still proves the loop is alive.
const DefaultHeartbeat = time.Hour

// QuietSink drops the steady-state chatter of a long-running loop: a poll and
// its following no-switch are forwarded only when something a reader would
// care about changed — the active account, its utilization (rounded to a
// whole percent), the threshold, or the no-switch decision — or when the
// heartbeat is due. Every other event kind passes straight through.
//
// A suppressed poll is held rather than discarded, so that if the decision
// that follows it did change, the reader still sees the summary it was made
// from, in order.
type QuietSink struct {
	next      EventSink
	heartbeat time.Duration

	mu           sync.Mutex
	lastPollKey  string
	lastPrinted  time.Time
	lastNoSwitch string
	held         *Event
}

// NewQuietSink wraps next. A heartbeat of zero or less uses DefaultHeartbeat.
func NewQuietSink(next EventSink, heartbeat time.Duration) *QuietSink {
	if heartbeat <= 0 {
		heartbeat = DefaultHeartbeat
	}
	return &QuietSink{next: next, heartbeat: heartbeat}
}

// Emit implements EventSink.
func (q *QuietSink) Emit(ev Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch ev.Kind {
	case "poll":
		q.held = nil
		key := pollKey(ev)
		if key == q.lastPollKey && ev.TS.Sub(q.lastPrinted) < q.heartbeat {
			held := ev
			q.held = &held
			return
		}
		q.lastPollKey = key
		q.lastPrinted = ev.TS
		q.next.Emit(ev)
	case "no-switch":
		key := ev.str("reason") + "\x00" + ev.str("detail")
		if q.held != nil && key == q.lastNoSwitch {
			q.held = nil // nothing changed this tick
			return
		}
		q.flush()
		q.lastNoSwitch = key
		q.next.Emit(ev)
	default:
		q.flush()
		q.next.Emit(ev)
	}
}

func (q *QuietSink) flush() {
	if q.held == nil {
		return
	}
	q.next.Emit(*q.held)
	q.lastPrinted = q.held.TS
	q.held = nil
}

// pollKey summarizes the parts of a poll that make it worth printing again.
// The other accounts' windows are deliberately left out: they drift a
// percent at a time across the whole roster and would defeat the filter.
func pollKey(ev Event) string {
	act, _ := ev.Fields["active"].(map[string]any)
	if act == nil {
		return "none"
	}
	num := anyInt(act["number"])
	email, _ := act["email"].(string)
	used := "unknown"
	if heads, ok := ev.Fields["headroomPct"].(map[string]*float64); ok {
		if h := heads[strconv.Itoa(num)]; h != nil {
			used = strconv.FormatFloat(math.Round(100-*h), 'f', 0, 64)
		}
	}
	return strconv.Itoa(num) + "\x00" + email + "\x00" + used + "\x00" + formatNum(ev.flt("threshold"))
}
