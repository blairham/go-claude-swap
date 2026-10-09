package autoswitch

import (
	"math"
	"time"

	"github.com/blairham/go-claude-swap/internal/switcher"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// Burn-rate tracking. A fast burn can carry the active account from below
// the threshold to 100% between two measurements (#15: 89% → 100% inside
// one poll), and then the only switch left is the at-limit one. Tracking
// the measured rate lets the engine see that coming and switch early.
const (
	// burnWindow is how much measurement history the slope is fitted over.
	burnWindow = 20 * time.Minute
	// burnMinSpan: two measurements closer than this give no usable slope.
	burnMinSpan = 60 * time.Second
)

type burnSample struct {
	at   time.Time
	util float64
}

// burnTracker holds recent distinct measurements of one account's binding
// utilization. It forgets everything when the active account changes or the
// utilization drops (a window reset makes the old slope meaningless).
type burnTracker struct {
	slot    int
	samples []burnSample
}

// observe records the measurement taken at `at`. Re-serving the same cached
// measurement on a later tick is a no-op, so the slope is over real fetches.
func (b *burnTracker) observe(slot int, at time.Time, util float64) {
	if slot != b.slot {
		b.slot, b.samples = slot, nil
	}
	if n := len(b.samples); n > 0 {
		last := b.samples[n-1]
		if !at.After(last.at) {
			return
		}
		if util < last.util {
			b.samples = nil
		}
	}
	b.samples = append(b.samples, burnSample{at: at, util: util})
	cut := at.Add(-burnWindow)
	i := 0
	for i < len(b.samples)-1 && b.samples[i].at.Before(cut) {
		i++
	}
	b.samples = b.samples[i:]
}

// rate is the burn in percentage points per second: the steeper of the
// window-wide slope and the latest pair's, so an acceleration shows at once
// while integer-quantized readings that sat still for one poll do not hide
// a steady climb. ok=false when there is no usable history.
func (b *burnTracker) rate() (float64, bool) {
	n := len(b.samples)
	if n < 2 {
		return 0, false
	}
	slope := func(a, z burnSample) (float64, bool) {
		span := z.at.Sub(a.at)
		if span < burnMinSpan {
			return 0, false
		}
		return (z.util - a.util) / span.Seconds(), true
	}
	best, ok := slope(b.samples[0], b.samples[n-1])
	if r, rok := slope(b.samples[n-2], b.samples[n-1]); rok && (!ok || r > best) {
		best, ok = r, true
	}
	return best, ok
}

// projectCrossing records the active account's current measurement and
// reports whether, at its burn rate, it will be past the threshold before
// the engine can act on the next fresh measurement: the next planned fetch
// of the active row, plus one tick to notice it.
func (e *Engine) projectCrossing(active *switcher.Snapshot, activeH *float64, now time.Time) bool {
	if activeH == nil || math.IsInf(active.Age, 1) {
		return false
	}
	util := 100 - *activeH
	measuredAt := now.Add(-time.Duration(active.Age * float64(time.Second)))
	e.burn.observe(active.Slot, measuredAt, util)
	if util >= e.Threshold {
		return false // already past: the proactive path owns it
	}
	r, ok := e.burn.rate()
	if !ok || r <= 0 {
		return false
	}
	return util+r*e.lookahead(active, measuredAt, now) >= e.Threshold
}

// lookahead is the seconds until the engine acts on the active account's
// next fresh measurement.
func (e *Engine) lookahead(active *switcher.Snapshot, measuredAt, now time.Time) float64 {
	nowS := float64(now.UnixNano()) / 1e9
	next := active.NextPollAt
	if next == 0 {
		// No plan: the row is re-fetched once it stops being fresh.
		next = float64(measuredAt.UnixNano())/1e9 + usage.ServeTTL
	}
	return math.Max(next-nowS, 0) + e.Interval
}
