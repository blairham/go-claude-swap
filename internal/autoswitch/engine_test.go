package autoswitch

import (
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

// testEngine is an engine at the shipped defaults: threshold 90, 60s
// interval, 10pt hysteresis, best strategy.
func testEngine(t *testing.T) *Engine {
	t.Helper()
	return NewEngine(Config{
		Threshold: 90, Interval: 60, Cooldown: 300, Hysteresis: 10,
		Strategy: strategyBest, UnhealthyTicks: 3,
	}, nil)
}

func hp(v float64) *float64 { return &v }

// cand builds an OAuth candidate; headroom nil means unknown.
func cand(slot int, headroom *float64) candidate {
	return candidate{slot: slot, acct: &account.Account{Email: "a@example.com"}, headroom: headroom}
}

func slots(cs []candidate) []int {
	out := make([]int, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.slot)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var activeSnap = &switcher.Snapshot{Slot: 3}

// The log case from #14: the active account is past the threshold, every
// other account is at or above it, and the old engine waited for 100% before
// switching to an account it had refused minutes earlier.
func TestLeastBadSwitchesBeforeTheLimit(t *testing.T) {
	e := testEngine(t)
	cands := []candidate{
		cand(2, hp(10)), // 7d 90%: unhealthy, but more room than the active
		cand(5, hp(8)),
		cand(7, nil),   // unknown never ranks
		cand(9, hp(0)), // exhausted never ranks
	}
	trigger, ranked := e.selectTargets(triggerProactive, cands, nil, hp(4), activeSnap)
	if trigger != triggerLeastBad {
		t.Fatalf("trigger = %q, want %q", trigger, triggerLeastBad)
	}
	if got := slots(ranked); !equalInts(got, []int{2, 5}) {
		t.Fatalf("ranked = %v, want [2 5]", got)
	}
}

// A healthy candidate still wins through the normal proactive ranking.
func TestLeastBadDoesNotPreemptHealthyRanking(t *testing.T) {
	e := testEngine(t)
	cands := []candidate{cand(2, hp(10)), cand(4, hp(40))}
	trigger, ranked := e.selectTargets(triggerProactive, cands, nil, hp(5), activeSnap)
	if trigger != triggerProactive || !equalInts(slots(ranked), []int{4}) {
		t.Fatalf("got %q %v, want proactive [4]", trigger, slots(ranked))
	}
}

// Near-equal accounts must not trade places: the margin is the hysteresis,
// shrunk to half the active account's headroom, and the gain must be strict.
func TestLeastBadHysteresis(t *testing.T) {
	cases := []struct {
		name       string
		hysteresis float64
		activeH    float64
		candH      float64
		want       bool
	}{
		{"just past threshold, 1pt better", 10, 9, 10, false}, // margin 4.5
		{"near the wall, 1pt better", 10, 5, 6, false},        // margin 2.5
		{"near the wall, clearly better", 10, 4, 10, true},    // margin 2
		{"margin met exactly", 10, 4, 6, true},
		{"zero hysteresis, equal", 0, 5, 5, false},
		{"zero hysteresis, strictly better", 0, 5, 5.1, true},
		{"small hysteresis caps the margin", 1, 8, 9, true},
	}
	for _, c := range cases {
		e := testEngine(t)
		e.Hysteresis = c.hysteresis
		got := len(e.leastBad([]candidate{cand(2, hp(c.candH))}, hp(c.activeH))) > 0
		if got != c.want {
			t.Errorf("%s: leastBad = %v, want %v", c.name, got, c.want)
		}
	}
}

// Least-bad is a proactive-only fallback: an at-limit tick keeps its own
// ranking, and a below-threshold consume-first tick never takes it.
func TestLeastBadOnlyForProactive(t *testing.T) {
	e := testEngine(t)
	cands := []candidate{cand(2, hp(0)), cand(5, hp(0))}
	if trig, ranked := e.selectTargets(triggerAtLimit, cands, nil, hp(0), activeSnap); len(ranked) != 0 || trig != triggerAtLimit {
		t.Fatalf("at-limit over exhausted candidates = %q %v, want none", trig, slots(ranked))
	}
	e.Strategy = strategyConsumeFirst
	cands = []candidate{cand(2, hp(50))}
	if trig, ranked := e.selectTargets(triggerConsumeFirst, cands, nil, hp(30), activeSnap); len(ranked) != 0 || trig != triggerConsumeFirst {
		t.Fatalf("consume-first = %q %v, want none", trig, slots(ranked))
	}
}

func TestLeastBadHonorsCooldown(t *testing.T) {
	if !proactiveLike(triggerLeastBad) {
		t.Fatal("least-bad is discretionary and must honor the cooldown")
	}
}

// When nothing qualifies while the active account is past the threshold,
// the runner must keep the normal interval rather than the 300s backoff.
func TestNoQualifyingPastThresholdKeepsPolling(t *testing.T) {
	e := testEngine(t)
	now := time.Unix(1_800_000_000, 0)
	res := e.nothingRanked(triggerProactive, &switcher.Snapshot{Slot: 3}, hp(9), []candidate{cand(2, hp(10))}, now)
	if res.outcome != OutcomeBlocked || !res.keepPolling {
		t.Fatalf("proactive no-qualifying = %+v, want Blocked that keeps polling", res)
	}
	assertInterval(t, e, res, now, "proactive no-qualifying")
}

func assertInterval(t *testing.T, e *Engine, res tickResult, now time.Time, what string) {
	t.Helper()
	if d := e.delayAfter(res, now); d < 0.9*e.Interval || d > 1.1*e.Interval {
		t.Errorf("%s: delay = %vs, want the %vs interval ±10%% (outcome %v)", what, d, e.Interval, res.outcome)
	}
}

// #39: a Blocked tick while the active account still has headroom keeps the
// normal interval under every strategy — whether no account is switchable at
// all, nothing qualifies, or every other account is exhausted. Backing off
// would delay the threshold check and the burn-rate projection by minutes.
func TestBlockedWithHeadroomNeverBacksOff(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	// The active account is 40% used with a weekly reset after every
	// candidate's, so consume-first could rank them if they were healthy.
	active := &switcher.Snapshot{Slot: 3, Usage: weekly(0, 40, 150, now)}
	unhealthy := []candidate{pacedCand(2, weekly(0, 95, 10, now))}
	exhausted := []candidate{pacedCand(2, weekly(0, 100, 0.5, now))}

	cases := []struct {
		strategy string
		trigger  string
		activeH  float64
	}{
		{strategyBest, triggerProjected, 60},
		{strategyBest, triggerProactive, 5},
		{strategyConsumeFirst, triggerConsumeFirst, 60},
		{strategyConsumeFirst, triggerProjected, 60},
		{strategyConsumeFirst, triggerProactive, 5},
		{strategyBalance, triggerBalance, 60},
		{strategyBalance, triggerProjected, 60},
		{strategyBalance, triggerProactive, 5},
	}
	for _, c := range cases {
		e := testEngine(t)
		e.Strategy = c.strategy
		name := c.strategy + "/" + c.trigger
		assertInterval(t, e, e.noCandidates(c.trigger), now, name+" no-candidates")
		assertInterval(t, e, e.nothingRanked(c.trigger, active, hp(c.activeH), unhealthy, now), now, name+" no-qualifying")
		if c.trigger == triggerProactive {
			continue // past the threshold, all-exhausted waits for recovery (below)
		}
		assertInterval(t, e, e.nothingRanked(c.trigger, active, hp(c.activeH), exhausted, now), now, name+" others exhausted")
	}
}

// The backoff and the recovery wait are for an active account that cannot be
// worked on, and for a past-threshold account with nothing to move to before
// a known recovery.
func TestBlockedBackoffStillApplies(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	e := testEngine(t)
	active := &switcher.Snapshot{Slot: 3}
	for _, trig := range []string{triggerAtLimit, triggerFailover} {
		if d := e.delayAfter(e.noCandidates(trig), now); d != 300 {
			t.Errorf("%s no-candidates: delay = %vs, want 300s", trig, d)
		}
	}
	exhausted := []candidate{pacedCand(2, weekly(0, 100, 0.5, now))}
	res := e.nothingRanked(triggerProactive, active, hp(5), exhausted, now)
	if res.keepPolling || res.recoverAt != now.Add(30*time.Minute).Unix() {
		t.Fatalf("proactive, others exhausted = %+v, want the recovery wait", res)
	}
}
