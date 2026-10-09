package autoswitch

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

type recordSink struct{ events []Event }

func (r *recordSink) Emit(ev Event) { r.events = append(r.events, ev) }

func (r *recordSink) kinds() string {
	ks := make([]string, 0, len(r.events))
	for _, e := range r.events {
		ks = append(ks, e.Kind)
	}
	return strings.Join(ks, ",")
}

func pollAt(ts time.Time, slot int, usedPct float64) Event {
	h := 100 - usedPct
	return Event{Kind: "poll", TS: ts, Fields: map[string]any{
		"threshold":   90.0,
		"active":      map[string]any{"number": slot, "email": "a@example.com"},
		"headroomPct": map[string]*float64{"1": &h, "2": &h},
	}}
}

func noSwitchAt(ts time.Time, reason, detail string) Event {
	f := map[string]any{"reason": reason}
	if detail != "" {
		f["detail"] = detail
	}
	return Event{Kind: "no-switch", TS: ts, Fields: f}
}

func TestQuietSinkSuppressesUnchangedTicks(t *testing.T) {
	rec := &recordSink{}
	q := NewQuietSink(rec, time.Hour)
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	tick := func(ts time.Time, used float64, reason, detail string) {
		q.Emit(pollAt(ts, 1, used))
		q.Emit(noSwitchAt(ts, reason, detail))
	}

	tick(t0, 40, "below-threshold", "")
	tick(t0.Add(time.Minute), 40.2, "below-threshold", "")   // rounds to same pct
	tick(t0.Add(2*time.Minute), 40.4, "below-threshold", "") // still 40
	if got := rec.kinds(); got != "poll,no-switch" {
		t.Fatalf("unchanged ticks must be suppressed; got %s", got)
	}

	tick(t0.Add(3*time.Minute), 41, "below-threshold", "") // utilization moved
	if got := rec.kinds(); got != "poll,no-switch,poll,no-switch" {
		t.Fatalf("changed utilization must print poll and decision; got %s", got)
	}
}

func TestQuietSinkDecisionChangeReleasesHeldPoll(t *testing.T) {
	rec := &recordSink{}
	q := NewQuietSink(rec, time.Hour)
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	q.Emit(pollAt(t0, 1, 50))
	q.Emit(noSwitchAt(t0, "below-threshold", ""))
	t1 := t0.Add(time.Minute)
	q.Emit(pollAt(t1, 1, 50)) // same summary: held
	q.Emit(noSwitchAt(t1, "cooldown", "30s left"))

	if got := rec.kinds(); got != "poll,no-switch,poll,no-switch" {
		t.Fatalf("a changed decision must print with its poll; got %s", got)
	}
	if !rec.events[2].TS.Equal(t1) {
		t.Fatalf("held poll should be the current tick's, got %v", rec.events[2].TS)
	}
}

func TestQuietSinkActiveAccountChangePrints(t *testing.T) {
	rec := &recordSink{}
	q := NewQuietSink(rec, time.Hour)
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	q.Emit(pollAt(t0, 1, 50))
	q.Emit(noSwitchAt(t0, "below-threshold", ""))
	q.Emit(pollAt(t0.Add(time.Minute), 2, 50))
	q.Emit(noSwitchAt(t0.Add(time.Minute), "below-threshold", ""))
	if got := rec.kinds(); got != "poll,no-switch,poll,no-switch" {
		t.Fatalf("a different active account must print; got %s", got)
	}
}

func TestQuietSinkHeartbeat(t *testing.T) {
	rec := &recordSink{}
	q := NewQuietSink(rec, time.Hour)
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	for i := 0; i <= 60; i++ {
		ts := t0.Add(time.Duration(i) * time.Minute)
		q.Emit(pollAt(ts, 1, 10))
		q.Emit(noSwitchAt(ts, "below-threshold", ""))
	}
	// Minute 0 and minute 60 (the heartbeat) print; the 59 in between do not.
	if got := rec.kinds(); got != "poll,no-switch,poll,no-switch" {
		t.Fatalf("want first tick plus one hourly heartbeat; got %s", got)
	}
}

func TestQuietSinkPassesOtherEventsAndFlushesHeldPoll(t *testing.T) {
	rec := &recordSink{}
	q := NewQuietSink(rec, time.Hour)
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	q.Emit(pollAt(t0, 1, 95))
	q.Emit(noSwitchAt(t0, "cooldown", ""))
	q.Emit(pollAt(t0.Add(time.Minute), 1, 95)) // held
	q.Emit(Event{Kind: "switch", TS: t0.Add(time.Minute), Fields: map[string]any{"from": 1, "to": 2}})
	q.Emit(Event{Kind: "error", TS: t0.Add(2 * time.Minute), Fields: map[string]any{"message": "x"}})
	if got := rec.kinds(); got != "poll,no-switch,poll,switch,error" {
		t.Fatalf("got %s", got)
	}
}

func TestHumanSinkDatedTimestamp(t *testing.T) {
	var buf bytes.Buffer
	s := NewHumanSink(&buf)
	ts := time.Date(2026, 10, 8, 12, 34, 56, 0, time.Local)
	s.Emit(Event{Kind: "no-switch", TS: ts, Fields: map[string]any{"reason": "below-threshold"}})
	want := ts.Format(time.RFC3339) + "  no switch: below-threshold\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
	if !strings.HasPrefix(buf.String(), "2026-10-08T12:34:56") {
		t.Fatalf("line must carry the date: %q", buf.String())
	}
}
