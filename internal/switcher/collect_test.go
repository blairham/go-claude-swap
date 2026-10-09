package switcher

import (
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/usage"
)

// The active row follows its poll plan even while fresh: the planner's
// urgent cadence is shorter than the serve TTL, and serving the fresh row
// first would silently stretch it back to the TTL.
func TestServesFreshHonorsActivePlan(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(d time.Duration) *float64 { v := float64(now.Add(d).Unix()); return &v }
	fetched := float64(now.Add(-100 * time.Second).Unix()) // fresh: under the 180s TTL

	due := &usage.Entry{FetchedAt: fetched, NextPollAt: at(-10 * time.Second)}
	notDue := &usage.Entry{FetchedAt: fetched, NextPollAt: at(50 * time.Second)}
	noPlan := &usage.Entry{FetchedAt: fetched}
	stale := &usage.Entry{FetchedAt: float64(now.Add(-time.Hour).Unix())}

	cases := []struct {
		name   string
		entry  *usage.Entry
		active bool
		want   bool
	}{
		{"active, plan due", due, true, false},
		{"active, plan not due", notDue, true, true},
		{"active, no plan", noPlan, true, true},
		{"inactive, plan due", due, false, true},
		{"stale", stale, true, false},
		{"no row", nil, true, false},
	}
	for _, c := range cases {
		if got := servesFresh(c.entry, c.active, now); got != c.want {
			t.Errorf("%s: servesFresh = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCollectorThreshold(t *testing.T) {
	if got := (&Collector{}).threshold(); got != 90 {
		t.Errorf("default threshold = %v, want 90", got)
	}
	if got := (&Collector{Threshold: 75}).threshold(); got != 75 {
		t.Errorf("threshold = %v, want 75", got)
	}
}
