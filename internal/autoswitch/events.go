package autoswitch

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
)

// Event is one engine occurrence: a kind, a timestamp, and structured
// fields. Field keys are camelCase so the JSON sink can emit them verbatim.
type Event struct {
	Kind   string
	TS     time.Time
	Fields map[string]any
}

// EventSink receives engine events. The engine emits from a single
// goroutine; sinks that are shared more widely must synchronize internally.
type EventSink interface {
	Emit(Event)
}

// Human renders the event as a one-line human string (no timestamp).
func (e Event) Human() string {
	switch e.Kind {
	case "poll":
		return e.humanPoll()
	case "switch":
		return e.humanSwitch()
	case "no-switch":
		if detail := e.str("detail"); detail != "" {
			return fmt.Sprintf("no switch: %s (%s)", e.str("reason"), detail)
		}
		return "no switch: " + e.str("reason")
	case "account-quarantined":
		return fmt.Sprintf("Account-%d (%s) quarantined: %s", e.num("number"), e.str("email"), e.str("reason"))
	case "account-unquarantined":
		return fmt.Sprintf("Account-%d (%s) released from quarantine (%s)", e.num("number"), e.str("email"), e.str("reason"))
	case "all-exhausted":
		if iso := e.str("earliestResetAt"); iso != "" {
			return "all accounts exhausted — earliest recovery " + iso
		}
		return "all accounts exhausted"
	case "sleep":
		until := e.str("until")
		if t, err := time.Parse(account.TimeFormat, until); err == nil {
			until = t.Local().Format("15:04:05")
		}
		return fmt.Sprintf("sleeping %ss (until %s)", formatNum(e.flt("seconds")), until)
	case "error":
		msg := "error: " + e.str("message")
		if b, _ := e.Fields["transient"].(bool); b {
			msg += " (transient)"
		}
		return msg
	case "config-warning":
		return "config warning: " + e.str("message")
	default:
		return e.Kind
	}
}

func (e Event) humanPoll() string {
	act, _ := e.Fields["active"].(map[string]any)
	if act == nil {
		return "poll: no active account"
	}
	num := anyInt(act["number"])
	email, _ := act["email"].(string)
	used := "usage unknown"
	if heads, ok := e.Fields["headroomPct"].(map[string]*float64); ok {
		if h := heads[strconv.Itoa(num)]; h != nil {
			used = formatNum(100-*h) + "% used"
		}
	}
	line := fmt.Sprintf("Account-%d (%s): %s (switch at %s%%)", num, email, used, formatNum(e.flt("threshold")))
	if others := e.pollOthers(num); others != "" {
		line += " | others: " + others
	}
	return line
}

func (e Event) pollOthers(activeNum int) string {
	windows, ok := e.Fields["windowsPct"].(map[string]map[string]float64)
	if !ok {
		return ""
	}
	var slots []int
	for key := range windows {
		if n, err := strconv.Atoi(key); err == nil && n != activeNum {
			slots = append(slots, n)
		}
	}
	sort.Ints(slots)
	parts := make([]string, 0, len(slots))
	for _, n := range slots {
		parts = append(parts, fmt.Sprintf("#%d: %s", n, formatWindows(windows[strconv.Itoa(n)])))
	}
	return strings.Join(parts, ", ")
}

func (e Event) humanSwitch() string {
	from, to := e.num("from"), e.num("to")
	trigger := e.str("trigger")
	if b, _ := e.Fields["dryRun"].(bool); b {
		return fmt.Sprintf("[dry-run] would switch Account-%d -> Account-%d (%s)", from, to, trigger)
	}
	return fmt.Sprintf("Switched Account-%d -> Account-%d (%s) (%s)", from, to, e.str("toEmail"), trigger)
}

func (e Event) str(key string) string {
	v, _ := e.Fields[key].(string)
	return v
}

func (e Event) num(key string) int { return anyInt(e.Fields[key]) }

func (e Event) flt(key string) float64 {
	switch v := e.Fields[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

func anyInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// formatNum renders a number rounded to one decimal with trailing zeros
// trimmed ("37", "99.9").
func formatNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}

// formatWindows renders one account's window pcts, 5h and 7d first, scoped
// names alphabetically after, joined with " · ".
func formatWindows(wm map[string]float64) string {
	var scoped []string
	for name := range wm {
		if name != "5h" && name != "7d" {
			scoped = append(scoped, name)
		}
	}
	sort.Strings(scoped)
	var ordered []string
	for _, n := range []string{"5h", "7d"} {
		if _, ok := wm[n]; ok {
			ordered = append(ordered, n)
		}
	}
	ordered = append(ordered, scoped...)
	segs := make([]string, 0, len(ordered))
	for _, n := range ordered {
		segs = append(segs, n+" "+formatNum(wm[n])+"%")
	}
	return strings.Join(segs, " · ")
}

// JSONSink writes each event as one JSON object per line:
// {"schemaVersion":1,"event":<kind>,"ts":<ISO seconds Z>, ...fields}.
type JSONSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONSink returns a JSONSink writing to w.
func NewJSONSink(w io.Writer) *JSONSink { return &JSONSink{w: w} }

// Emit writes the event as a JSON line. Write failures are swallowed: a
// broken pipe must never crash the loop.
func (s *JSONSink) Emit(ev Event) {
	obj := make(map[string]any, len(ev.Fields)+3)
	for k, v := range ev.Fields {
		obj[k] = v
	}
	obj["schemaVersion"] = 1
	obj["event"] = ev.Kind
	obj["ts"] = ev.TS.UTC().Format(account.TimeFormat)
	line, err := json.Marshal(obj)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(append(line, '\n'))
}

// HumanSink writes "HH:MM:SS  <human>" lines.
type HumanSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewHumanSink returns a HumanSink writing to w.
func NewHumanSink(w io.Writer) *HumanSink { return &HumanSink{w: w} }

// Emit writes the human rendering with a local-time prefix. Write failures
// are swallowed: a broken pipe must never crash the loop.
func (s *HumanSink) Emit(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = fmt.Fprintf(s.w, "%s  %s\n", ev.TS.Local().Format("15:04:05"), ev.Human())
}
