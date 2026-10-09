package autoswitch

import (
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/switcher"
)

var t0 = time.Unix(1_800_000_000, 0) // fake clock origin

func epoch(t time.Time) float64 { return float64(t.Unix()) }

// tickAt feeds the engine one tick's view of the active account: a
// measurement `age` seconds old, with the next fetch planned at nextPoll.
func tickAt(e *Engine, now time.Time, util, age float64, nextPoll time.Time) bool {
	snap := &switcher.Snapshot{Slot: 8, Age: age, NextPollAt: epoch(nextPoll)}
	return e.projectCrossing(snap, hp(100-util), now)
}

// The #15 shape: a fast burn heading for the threshold is caught one
// measurement early instead of being found at 100%.
func TestProjectedCrossingFastBurn(t *testing.T) {
	e := testEngine(t)
	if tickAt(e, t0, 71, 0, t0.Add(150*time.Second)) {
		t.Fatal("one measurement has no slope; must not project")
	}
	// The same cached measurement re-served a tick later is not new data.
	if tickAt(e, t0.Add(60*time.Second), 71, 60, t0.Add(150*time.Second)) {
		t.Fatal("re-served measurement must not create a slope")
	}
	// Accelerating: 74% then 80%; at each look the projection for the
	// next one (150s plan + one tick) stays below 90.
	if tickAt(e, t0.Add(150*time.Second), 74, 0, t0.Add(300*time.Second)) {
		t.Fatal("74% at 1.2pt/min must not project a crossing")
	}
	if tickAt(e, t0.Add(300*time.Second), 80, 0, t0.Add(450*time.Second)) {
		t.Fatal("80% at 2.4pt/min projects ~88% at the next look; must not switch")
	}
	// 89% and climbing 3.6pt/min: by the next look it is ~101%. The old
	// engine found it at 100% and switched at-limit; switch now.
	if !tickAt(e, t0.Add(450*time.Second), 89, 0, t0.Add(600*time.Second)) {
		t.Fatal("89% at 3.6pt/min must project past the threshold")
	}
}

// The look-ahead runs to the next planned fetch, not just the next tick: a
// tick that only re-serves the cached row cannot see the climb.
func TestProjectedCrossingLooksToNextFetch(t *testing.T) {
	e := testEngine(t)
	tickAt(e, t0, 80, 0, t0.Add(150*time.Second))
	// 86% at 2.4pt/min: ~88% after one tick, ~94% by the fetch 150s out.
	if !tickAt(e, t0.Add(150*time.Second), 86, 0, t0.Add(300*time.Second)) {
		t.Fatal("must project to the next planned fetch plus one tick")
	}
}

func TestProjectedCrossingSlowBurn(t *testing.T) {
	e := testEngine(t)
	for i, u := range []float64{85, 86, 87} {
		now := t0.Add(time.Duration(i) * 150 * time.Second)
		if tickAt(e, now, u, 0, now.Add(150*time.Second)) {
			t.Fatalf("slow burn at %v%% must not project a crossing", u)
		}
	}
}

func TestProjectedTriggerClassification(t *testing.T) {
	e := testEngine(t)
	if trig, _, done := e.decideTrigger(hp(15), true); done || trig != triggerProjected {
		t.Fatalf("below threshold + projected = %q done=%v, want %q", trig, done, triggerProjected)
	}
	if _, _, done := e.decideTrigger(hp(15), false); !done {
		t.Fatal("below threshold, not projected: no switch")
	}
	if trig, _, _ := e.decideTrigger(hp(5), true); trig != triggerProactive {
		t.Fatalf("past threshold = %q, want proactive", trig)
	}
	e.Strategy = strategyConsumeFirst
	if trig, _, _ := e.decideTrigger(hp(15), true); trig != triggerProjected {
		t.Fatalf("consume-first + projected = %q, want %q", trig, triggerProjected)
	}
	if !proactiveLike(triggerProjected) {
		t.Fatal("a projected switch is discretionary and must honor the cooldown")
	}
	res := e.nothingRanked(triggerProjected, &switcher.Snapshot{Slot: 8}, hp(15), []candidate{cand(2, hp(12))}, time.Now())
	if !res.keepPolling {
		t.Fatal("projected and nothing qualifies: keep polling, do not back off")
	}
}

func TestBurnTracker(t *testing.T) {
	var b burnTracker
	b.observe(1, t0, 50)
	b.observe(1, t0.Add(30*time.Second), 60)
	if _, ok := b.rate(); ok {
		t.Fatal("30s apart is under burnMinSpan; no slope")
	}
	b.observe(1, t0.Add(120*time.Second), 62)
	r, ok := b.rate()
	if !ok || r != 12.0/120 {
		t.Fatalf("rate = %v %v, want window slope %v", r, ok, 12.0/120)
	}
	// Acceleration: the latest pair is steeper than the window.
	b.observe(1, t0.Add(180*time.Second), 74)
	if r, _ := b.rate(); r != 12.0/60 {
		t.Fatalf("rate = %v, want latest-pair slope %v", r, 12.0/60)
	}
	// The same measurement re-served from cache on a later tick changes
	// nothing.
	b.observe(1, t0.Add(180*time.Second), 74)
	if r, _ := b.rate(); r != 12.0/60 || len(b.samples) != 4 {
		t.Fatalf("re-served sample changed the slope: rate %v, %d samples", r, len(b.samples))
	}
	// A drop is a window reset: history is discarded.
	b.observe(1, t0.Add(240*time.Second), 10)
	if _, ok := b.rate(); ok {
		t.Fatal("after a reset there is one sample; no slope")
	}
	// A different active account starts over.
	b.observe(1, t0.Add(400*time.Second), 20)
	b.observe(2, t0.Add(500*time.Second), 90)
	if _, ok := b.rate(); ok || len(b.samples) != 1 {
		t.Fatalf("slot change must reset history, have %d samples", len(b.samples))
	}
	// Samples older than the window are trimmed.
	b.observe(2, t0.Add(500*time.Second+burnWindow+time.Minute), 95)
	if len(b.samples) != 1 {
		t.Fatalf("stale samples kept: %d", len(b.samples))
	}
}
