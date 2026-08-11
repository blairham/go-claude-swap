package usage

import (
	"math"
	"testing"
	"time"
)

func TestHeadroomAndRelevantWindows(t *testing.T) {
	u := &Usage{
		FiveHour: &Window{Pct: 42},
		SevenDay: &Window{Pct: 63},
		Scoped:   []Window{{Name: "Fable", Pct: 88}, {Name: "Opus", Pct: 10}},
		Spend:    &Spend{Pct: 99}, // must never count
	}
	h, ok := u.Headroom(nil)
	if !ok || h != 37 {
		t.Errorf("headroom without models = %v %v, want 37", h, ok)
	}
	h, _ = u.Headroom([]string{"fable"})
	if h != 12 {
		t.Errorf("headroom with Fable = %v, want 12", h)
	}
	h, _ = u.Headroom([]string{"all"})
	if h != 12 {
		t.Errorf("headroom with all = %v, want 12", h)
	}
	if _, ok := (&Usage{Spend: &Spend{Pct: 50}}).Headroom(nil); ok {
		t.Error("spend-only usage must report unknown headroom")
	}
}

func TestFailureBackoff(t *testing.T) {
	cases := []struct {
		failures    int
		retryAfter  float64
		rateLimited bool
		want        float64
	}{
		{1, 0, false, 30},
		{2, 0, false, 60},
		{6, 0, false, 600}, // cap
		{1, 0, true, 300},  // Retry-After: 0 on a 429 → edge backoff
		{1, 120, true, 120},
		{1, 1000, true, 1900},  // >600 on 429 → +900 margin
		{1, 10000, true, 4500}, // park bound
		{1, 10000, false, 3600},
		{5, 60, false, 480}, // computed floor wins
	}
	for _, c := range cases {
		got := failureBackoff(c.failures, c.retryAfter, c.rateLimited)
		if got != c.want {
			t.Errorf("failureBackoff(%d, %v, %v) = %v, want %v", c.failures, c.retryAfter, c.rateLimited, got, c.want)
		}
	}
}

func TestEntryFreshAndDecisionValue(t *testing.T) {
	now := time.Unix(10_000, 0)
	u := &Usage{FiveHour: &Window{Pct: 50}}

	fresh := &Entry{LastGood: u, FetchedAt: float64(now.Unix()) - 100}
	if !fresh.Fresh(now) || fresh.DecisionValue(now, nil) == nil {
		t.Error("100s-old entry should be fresh and decision-grade")
	}

	stale := &Entry{LastGood: u, FetchedAt: float64(now.Unix()) - 400}
	if stale.Fresh(now) {
		t.Error("400s-old entry is not fresh")
	}
	if stale.DecisionValue(now, nil) != nil {
		t.Error("400s-old entry with no failure state is not decision-grade")
	}

	// Failure state extends trust up to the ceiling.
	staleFailing := &Entry{LastGood: u, FetchedAt: float64(now.Unix()) - 400, ConsecutiveFailures: 2}
	if staleFailing.DecisionValue(now, nil) == nil {
		t.Error("failing entry keeps lastGood decision-grade within the ceiling")
	}
}

func TestTokenDeadBindsToFingerprint(t *testing.T) {
	e := &Entry{AuthDeadStrikes: 1, StruckFingerprint: "sha256:aaa"}
	if !e.TokenDead("sha256:aaa") {
		t.Error("same generation → dead")
	}
	if e.TokenDead("sha256:bbb") {
		t.Error("rewritten credential heals the strike")
	}
	if (&Entry{}).TokenDead("sha256:aaa") {
		t.Error("no strikes → alive")
	}
}

func TestPlanAfterFetch(t *testing.T) {
	now := time.Unix(100_000, 0)

	// Movement halves the interval (floored).
	_, iv := PlanAfterFetch(PlanInput{Active: true, PrevInterval: 300, PrevPct: 10, NewPct: 15, Threshold: 90}, now)
	if iv != 180 {
		t.Errorf("moving interval = %v, want 180", iv)
	}
	// No movement grows it toward the ceiling.
	_, iv = PlanAfterFetch(PlanInput{Active: false, PrevInterval: 300, PrevPct: 10, NewPct: 10, Threshold: 90}, now)
	if iv != 450 {
		t.Errorf("idle interval = %v, want 450", iv)
	}
	// Urgent: active, moving, inside the escalation band.
	_, iv = PlanAfterFetch(PlanInput{Active: true, PrevInterval: 180, PrevPct: 70, NewPct: 80, Threshold: 90}, now)
	if iv != 60 {
		t.Errorf("urgent interval = %v, want 60", iv)
	}
	// Recent 429 suppresses urgency.
	_, iv = PlanAfterFetch(PlanInput{Active: true, PrevInterval: 180, PrevPct: 70, NewPct: 80, Threshold: 90, Recent429: true}, now)
	if iv < 360 {
		t.Errorf("post-429 interval = %v, want >= 360", iv)
	}
	// Exhausted floors at 600.
	_, iv = PlanAfterFetch(PlanInput{Active: false, PrevPct: 99, NewPct: 100, Threshold: 90, Exhausted: true}, now)
	if iv < 600 {
		t.Errorf("exhausted interval = %v, want >= 600", iv)
	}
	// Unknown movement uses the default.
	_, iv = PlanAfterFetch(PlanInput{Active: false, PrevPct: math.NaN(), NewPct: 50, Threshold: 90}, now)
	if iv != 300 {
		t.Errorf("default candidate interval = %v, want 300", iv)
	}
}

func TestFormatCountdown(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{12 * time.Minute, "12m"},
		{2*time.Hour + 13*time.Minute, "2h 13m"},
		{2 * time.Hour, "2h"},
		{76 * time.Hour, "3d 4h"},
		{72 * time.Hour, "3d"},
	}
	for _, c := range cases {
		if got := FormatCountdown(c.d); got != c.want {
			t.Errorf("FormatCountdown(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
