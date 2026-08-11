package usage

import (
	"math"
	"math/rand"
	"time"
)

// Poll-policy constants (seconds / percent). The usage endpoint budgets
// roughly 28–30 requests per trailing hour per identity, so the planner
// targets an average of one request every 3+ minutes and backs off hard
// after a 429.
const (
	// MinInterval is the movement-halving floor.
	MinInterval = 180.0
	// UrgentInterval applies when the active account is moving inside the
	// escalation band.
	UrgentInterval = 60.0
	// ActiveMaxInterval caps the active account's cadence.
	ActiveMaxInterval = 300.0
	// CandidateDefaultInterval is a candidate's starting cadence.
	CandidateDefaultInterval = 300.0
	// CandidateMaxInterval caps a candidate's cadence.
	CandidateMaxInterval = 600.0
	// ExhaustedInterval slow-probes at-limit accounts.
	ExhaustedInterval = 600.0
	// MovementDelta is the pct change that counts as movement.
	MovementDelta = 1.0
	// JitterFrac spreads polls ±10%.
	JitterFrac = 0.1
	// EscalationMargin: within this many pct of the threshold, poll urgently.
	EscalationMargin = 15.0
	// ResetSlack pads reset-aligned polls.
	ResetSlack = 60.0

	post429MinInterval = 360.0
	recent429Window    = 3600.0
	post429Mult        = 1.5
	post429MaxInterval = 1800.0
)

// PlanInput carries what the planner needs after a fetch.
type PlanInput struct {
	Active       bool
	PrevInterval float64 // 0 = none
	PrevPct      float64 // binding pct before this fetch; NaN = unknown
	NewPct       float64 // binding pct now; NaN = unknown
	Threshold    float64
	Recent429    bool
	Exhausted    bool  // headroom <= 0
	EarliestRst  int64 // earliest future relevant reset (epoch s), 0 = none
	LimitingRst  int64 // latest reset among >=100% windows, 0 = none
}

// PlanAfterFetch computes (nextPollAt epoch seconds, interval) for a row.
func PlanAfterFetch(in PlanInput, now time.Time) (float64, float64) {
	defaultIv := CandidateDefaultInterval
	ceiling := CandidateMaxInterval
	if in.Active {
		defaultIv = MinInterval
		ceiling = ActiveMaxInterval
	}
	base := in.PrevInterval
	if base == 0 {
		base = defaultIv
	}

	var interval float64
	movementKnown := !math.IsNaN(in.PrevPct) && !math.IsNaN(in.NewPct)
	switch {
	case !movementKnown:
		interval = defaultIv
	case math.Abs(in.NewPct-in.PrevPct) >= MovementDelta:
		interval = math.Max(MinInterval, base/2)
	default:
		// max(MinInterval, ·) snaps a previous urgent 60s back to normal.
		interval = math.Min(ceiling, math.Max(MinInterval, base*1.5))
	}

	moving := movementKnown && math.Abs(in.NewPct-in.PrevPct) >= MovementDelta
	if in.Active && moving && !in.Recent429 && !math.IsNaN(in.NewPct) && in.NewPct >= in.Threshold-EscalationMargin {
		interval = UrgentInterval
	}
	if in.Recent429 {
		increased := math.Max(base*post429Mult, post429MinInterval)
		interval = math.Min(post429MaxInterval, math.Max(interval, increased))
	}
	if in.Exhausted {
		interval = math.Max(interval, ExhaustedInterval)
	}

	next := float64(now.Unix()) + interval*(1+JitterFrac*(2*rand.Float64()-1))

	// Reset clamp: an exhausted account is worth re-checking right after its
	// limiting window resets; otherwise align to the earliest future reset.
	if in.Exhausted && in.LimitingRst > 0 {
		next = math.Min(next, float64(in.LimitingRst)+ResetSlack)
	} else if !in.Exhausted && in.EarliestRst > 0 {
		next = math.Min(next, float64(in.EarliestRst)+ResetSlack)
	}
	return next, interval
}

// Recent429 reports whether the row is inside the post-429 pacing window.
// Recency is measured from when an honored backoff lifts, not from the 429
// itself.
func (e *Entry) Recent429(now time.Time) bool {
	if e == nil || e.Last429At == nil {
		return false
	}
	anchor := *e.Last429At
	if e.LastError == "http-429" && e.BackoffUntil != nil && *e.BackoffUntil > anchor {
		anchor = *e.BackoffUntil
	}
	return float64(now.Unix()) < anchor+recent429Window
}

// PollDue reports whether a fetch is due per the persisted plan. A plan that
// "overslept" its interval (reset-parked far beyond cadence) is structurally
// obsolete and counts as due.
func (e *Entry) PollDue(now time.Time) bool {
	if e == nil || e.FetchedAt == 0 {
		return true
	}
	if e.InBackoff(now) {
		return false
	}
	if e.NextPollAt == nil {
		return !e.Fresh(now)
	}
	nowS := float64(now.Unix())
	if nowS >= *e.NextPollAt {
		return true
	}
	iv := CandidateMaxInterval
	if e.PollIntervalS != nil {
		iv = math.Max(*e.PollIntervalS, ExhaustedInterval)
	}
	return *e.NextPollAt > nowS+iv*1.1+ResetSlack
}
