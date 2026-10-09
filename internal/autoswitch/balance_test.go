package autoswitch

import (
	"math"
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/settings"
	"github.com/blairham/go-claude-swap/internal/switcher"
	"github.com/blairham/go-claude-swap/internal/usage"
)

var balanceNow = time.Unix(1_800_000_000, 0) // fake clock

func resetIn(now time.Time, hours float64) string {
	return now.Add(time.Duration(hours * float64(time.Hour))).UTC().Format(time.RFC3339)
}

// weekly builds usage with a 5h and a 7d window; the 7d resets in `hours`.
func weekly(fiveH, sevenD, hours float64, now time.Time) *usage.Usage {
	return &usage.Usage{
		FiveHour: &usage.Window{Pct: fiveH, ResetsAt: resetIn(now, 2)},
		SevenDay: &usage.Window{Pct: sevenD, ResetsAt: resetIn(now, hours)},
	}
}

// pacedCand is a candidate whose headroom is derived from its usage.
func pacedCand(slot int, u *usage.Usage) candidate {
	c := cand(slot, nil)
	h, _ := u.Headroom(nil)
	c.headroom, c.usage = &h, u
	return c
}

func balanceEngine(t *testing.T) *Engine {
	t.Helper()
	e := testEngine(t)
	e.Strategy = strategyBalance
	return e
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestWeeklyPace(t *testing.T) {
	now := balanceNow
	cases := []struct {
		name   string
		u      *usage.Usage
		models []string
		want   float64
	}{
		{"7d 90%, 10h left", weekly(0, 90, 10, now), nil, 1},
		{"5h is a burst limit, not a budget", weekly(99, 40, 60, now), nil, 1},
		{"watched model's weekly limit binds", &usage.Usage{
			SevenDay: &usage.Window{Pct: 20, ResetsAt: resetIn(now, 100)},
			Scoped:   []usage.Window{{Name: "Fable", Pct: 80, ResetsAt: resetIn(now, 10)}},
		}, []string{"Fable"}, 2},
		{"unknown reset counts as a full week", &usage.Usage{SevenDay: &usage.Window{Pct: 16}}, nil, 84.0 / 168},
		{"reset already due is floored at 1h", weekly(0, 50, -3, now), nil, 50},
		{"exhausted has no pace", weekly(0, 100, 10, now), nil, 0},
	}
	for _, c := range cases {
		got, ok := weeklyPace(c.u, c.models, now)
		if !ok || !approx(got, c.want) {
			t.Errorf("%s: pace = %v %v, want %v", c.name, got, ok, c.want)
		}
	}
	if _, ok := weeklyPace(&usage.Usage{FiveHour: &usage.Window{Pct: 10}}, nil, now); ok {
		t.Error("no weekly window: pace must be unknown")
	}
}

// The #16 shape: the active account still has room, but another healthy
// account's weekly budget expires much sooner. Balance moves there now
// instead of draining the active account to the threshold first.
func TestBalanceRebalancesBelowThreshold(t *testing.T) {
	now := balanceNow
	e := balanceEngine(t)
	active := &switcher.Snapshot{Slot: 3, Usage: weekly(10, 60, 100, now)} // pace 0.4/h
	cands := []candidate{
		pacedCand(2, weekly(5, 50, 20, now)),  // pace 2.5/h: expiring room
		pacedCand(7, weekly(0, 10, 160, now)), // pace 0.56/h: under 1.5x
		pacedCand(8, weekly(95, 20, 10, now)), // pace 8/h but 5h-unhealthy
		pacedCand(9, weekly(0, 30, 20, now)),  // pace 3.5/h: best
	}
	got := slots(e.rankBalance(triggerBalance, cands, hp(40), active, now))
	if !equalInts(got, []int{9, 2}) {
		t.Fatalf("balance ranking = %v, want [9 2]", got)
	}
}

// Near-equal pace never rebalances: the ratio is the anti-ping-pong guard.
func TestBalanceRatio(t *testing.T) {
	now := balanceNow
	e := balanceEngine(t)
	active := &switcher.Snapshot{Slot: 3, Usage: weekly(0, 60, 100, now)} // 0.4/h
	under := pacedCand(2, weekly(0, 44, 100, now))                        // 0.56/h = 1.4x
	at := pacedCand(4, weekly(0, 40, 100, now))                           // 0.6/h = 1.5x
	if got := slots(e.rankBalance(triggerBalance, []candidate{under, at}, hp(40), active, now)); !equalInts(got, []int{4}) {
		t.Fatalf("ranking = %v, want only the 1.5x candidate [4]", got)
	}
	// An active account with no weekly data cannot be compared.
	blind := &switcher.Snapshot{Slot: 3}
	if got := e.rankBalance(triggerBalance, []candidate{at}, hp(40), blind, now); len(got) != 0 {
		t.Fatalf("unknown active pace must not rebalance, got %v", slots(got))
	}
}

// Past the threshold, balance keeps the headroom hysteresis but orders by
// pace, where best orders by raw headroom.
func TestBalanceProactiveOrdersByPace(t *testing.T) {
	now := balanceNow
	e := balanceEngine(t)
	active := &switcher.Snapshot{Slot: 3, Usage: weekly(0, 92, 50, now)}
	roomy := pacedCand(2, weekly(0, 40, 150, now))   // 60 room, 0.4/h
	expiring := pacedCand(5, weekly(0, 70, 10, now)) // 30 room, 3/h
	close := pacedCand(6, weekly(0, 85, 2, now))     // 15 room: fails hysteresis (needs 18)
	cands := []candidate{roomy, expiring, close}
	if got := slots(e.rankBalance(triggerProactive, cands, hp(8), active, now)); !equalInts(got, []int{5, 2}) {
		t.Fatalf("balance proactive = %v, want [5 2]", got)
	}
	e.Strategy = strategyBest
	if got := slots(e.rank(triggerProactive, cands, hp(8), active)); !equalInts(got, []int{2, 5}) {
		t.Fatalf("best proactive = %v, want [2 5] (unchanged)", got)
	}
}

func TestBalanceTriggerWiring(t *testing.T) {
	e := balanceEngine(t)
	if trig, _, done := e.decideTrigger(hp(40), false); done || trig != triggerBalance {
		t.Fatalf("balance below threshold = %q done=%v, want %q", trig, done, triggerBalance)
	}
	if trig, _, _ := e.decideTrigger(hp(5), false); trig != triggerProactive {
		t.Fatalf("balance past threshold = %q, want proactive", trig)
	}
	if !proactiveLike(triggerBalance) {
		t.Fatal("a rebalance is discretionary and must honor the cooldown")
	}
	// A rebalance never falls back to API-key accounts or least-bad.
	api := []candidate{cand(11, nil)}
	now := time.Now()
	active := &switcher.Snapshot{Slot: 3, Usage: weekly(0, 10, 150, now)}
	if trig, ranked := e.selectTargets(triggerBalance, []candidate{pacedCand(2, weekly(0, 20, 150, now))}, api, hp(90), active); len(ranked) != 0 {
		t.Fatalf("balance with nothing ahead = %q %v, want nothing", trig, slots(ranked))
	}
	// Nothing ahead is the steady state, not a block: no backoff.
	res := e.nothingRanked(triggerBalance, active, hp(90), []candidate{pacedCand(2, weekly(0, 20, 150, now))}, now)
	if res.outcome != OutcomeNoAction {
		t.Fatalf("balance with nothing ahead = outcome %v, want NoAction", res.outcome)
	}
	if d := e.delayAfter(res, now); d > 1.1*e.Interval {
		t.Fatalf("balance with nothing ahead slept %vs; must keep the %vs interval", d, e.Interval)
	}
}

func TestBalanceStrategyAccepted(t *testing.T) {
	var warned []string
	e := NewEngine(Config{
		Threshold: 90, Interval: 60, Hysteresis: 10, Strategy: strategyBalance, UnhealthyTicks: 3,
	}, sinkFunc(func(ev Event) { warned = append(warned, ev.Kind) }))
	if e.Strategy != strategyBalance || len(warned) != 0 {
		t.Fatalf("strategy = %q warnings %v; balance must be accepted as-is", e.Strategy, warned)
	}
	if _, err := settings.ParseStrict("autoswitch.strategy", "balance"); err != nil {
		t.Fatalf("config set autoswitch.strategy balance: %v", err)
	}
}

type sinkFunc func(Event)

func (f sinkFunc) Emit(ev Event) { f(ev) }
