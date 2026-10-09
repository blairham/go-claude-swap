// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package autoswitch

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/blairham/go-claude-swap/internal/switcher"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// The balance strategy paces weekly usage across the roster instead of
// draining each account to the threshold in turn (#16). An account's pace is
// its weekly headroom per hour left until that weekly window resets: the
// share of its budget it can spend each hour and still have used all of it
// when the week rolls over. Rotating to the highest pace spends the room
// that would otherwise expire unused, and leaves the accounts that reset
// later for later in the week.
const (
	// balanceRatio: a below-threshold rebalance needs a healthy candidate
	// whose pace is at least this multiple of the active account's. Pace
	// moves slowly and the active account's falls as it is used, so the
	// ratio, not the cooldown, is what stops switching back and forth.
	balanceRatio = 1.5

	weekHours      = 7 * 24.0
	minPaceHorizon = 1.0 // hours; a window about to reset is not infinitely urgent
)

// weeklyPace is the weekly headroom per remaining hour for u: the binding
// (fullest) weekly window among the relevant ones — 7d and any watched
// model's weekly limit; 5h is a burst limit, not a budget — divided by the
// hours until that window resets. An unknown reset counts as a full week.
func weeklyPace(u *usage.Usage, models []string, now time.Time) (float64, bool) {
	if u == nil {
		return 0, false
	}
	var binding *usage.Window
	for _, w := range u.RelevantWindows(models) {
		if w.Name == "5h" {
			continue
		}
		if binding == nil || w.Pct > binding.Pct {
			binding = &w
		}
	}
	if binding == nil {
		return 0, false
	}
	hours := weekHours
	if r := usage.ParseReset(binding.ResetsAt); r > 0 {
		hours = math.Min(math.Max(float64(r-now.Unix())/3600, minPaceHorizon), weekHours)
	}
	return math.Max(100-binding.Pct, 0) / hours, true
}

// rankBalance orders candidates for the balance strategy, highest pace
// first. Every target must land healthy. Past the threshold (proactive) it
// also needs the usual headroom hysteresis; below it (a rebalance) it needs
// balanceRatio times the active account's pace instead.
func (e *Engine) rankBalance(
	trigger string,
	cands []candidate,
	activeH *float64,
	active *switcher.Snapshot,
	now time.Time,
) []candidate {
	var activePace float64
	if trigger == triggerBalance {
		p, ok := weeklyPace(active.Usage, e.models, now)
		if !ok {
			return nil
		}
		activePace = p
	}
	type paced struct {
		c    candidate
		pace float64
	}
	var ps []paced
	for _, c := range cands {
		if c.headroom == nil || *c.headroom <= 0 || 100-*c.headroom >= e.Threshold {
			continue
		}
		p, ok := weeklyPace(c.usage, e.models, now)
		if !ok {
			continue
		}
		if trigger == triggerBalance {
			// The epsilon keeps an exact 1.5x from failing on float rounding.
			if p+1e-9 < activePace*balanceRatio || p <= activePace {
				continue
			}
		} else if activeH != nil && *c.headroom-*activeH < e.Hysteresis {
			continue
		}
		ps = append(ps, paced{c, p})
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].pace != ps[j].pace {
			return ps[i].pace > ps[j].pace
		}
		return ps[i].c.slot < ps[j].c.slot
	})
	out := make([]candidate, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.c)
	}
	return out
}

// balanced reports a rebalance tick that found nothing worth moving to. It
// is the steady state of the strategy, not a block: the runner keeps its
// normal interval so threshold and burn-rate checks are not delayed.
func (e *Engine) balanced(active *switcher.Snapshot, now time.Time) tickResult {
	detail := ""
	if p, ok := weeklyPace(active.Usage, e.models, now); ok {
		detail = fmt.Sprintf("active pace %s%%/h", formatNum(p))
	}
	e.noSwitch("balanced", detail)
	return result(OutcomeNoAction)
}
