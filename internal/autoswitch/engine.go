// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package autoswitch implements the cswap auto loop: polling per-account
// usage, deciding when the active account should be rotated, and performing
// the switch — with cooldown, hysteresis, and quarantine guards persisted in
// autoswitch_state.json under the state lock.
package autoswitch

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/settings"
	"github.com/blairham/go-claude-swap/internal/switcher"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// Strategy and trigger names. A strategy is configuration; a trigger is why
// this particular tick wants to switch.
const (
	strategyBest         = "best"
	strategyConsumeFirst = "consume-first"
	strategyBalance      = "balance"

	triggerProactive    = "proactive"
	triggerAtLimit      = "at-limit"
	triggerFailover     = "failover"
	triggerConsumeFirst = "consume-first"
	// triggerBalance: balance strategy, active below the threshold, and a
	// healthy candidate's weekly pace is well ahead of the active account's.
	triggerBalance = "balance"
	// triggerLeastBad: the active account is past the threshold, no
	// candidate is healthy, and one has clearly more headroom — move to it
	// now rather than riding the active account to 100%.
	triggerLeastBad = "least-bad"
	// triggerProjected: the active account is still below the threshold,
	// but at its measured burn rate it will be past it before the engine
	// next sees a fresh measurement — switch now rather than at 100%.
	triggerProjected = "projected"
)

// freshenLead plus oauth.ExpiryBuffer gives the 10-minute pre-switch
// freshness margin: a target whose token expires within 10 minutes is
// refreshed before its credential goes live.
const freshenLead = 10*time.Minute - oauth.ExpiryBuffer

// Outcome classifies one tick; the values double as `auto --once` exit codes.
type Outcome int

// Outcome values.
const (
	OutcomeSwitched Outcome = 0
	OutcomeError    Outcome = 1
	OutcomeNoAction Outcome = 2
	OutcomeBlocked  Outcome = 3
)

// Config is the effective auto-switch tuning for one engine.
type Config struct {
	Threshold      float64  // switch when active utilization reaches this pct
	Interval       float64  // seconds between polls
	Cooldown       float64  // min seconds between proactive switches
	Hysteresis     float64  // target must beat active headroom by this many pct
	Strategy       string   // "best", "consume-first", or "balance"
	IncludeAPIKey  bool     // allow rotating onto managed API-key accounts
	UnhealthyTicks int      // unknown-headroom ticks before failover
	Models         []string // raw list; may contain the "auto"/"none" sentinels
	DryRun         bool
}

// ConfigFromSettings builds the Config from settings.json; CLI overrides
// are applied on top by the caller before NewEngine.
func ConfigFromSettings() Config {
	s := settings.Load()
	return Config{
		Threshold:      s.Float("autoswitch.threshold"),
		Interval:       s.Float("autoswitch.intervalSeconds"),
		Cooldown:       s.Float("autoswitch.cooldownSeconds"),
		Hysteresis:     s.Float("autoswitch.hysteresisPct"),
		Strategy:       s.String("autoswitch.strategy"),
		IncludeAPIKey:  s.Bool("autoswitch.includeApiKeyAccounts"),
		UnhealthyTicks: s.Int("autoswitch.unhealthyTicks"),
		Models:         s.Models(),
	}
}

// SetModels overrides the model list from a raw comma-separated string.
func (c *Config) SetModels(raw string) {
	c.Models = settings.ParseModelNames(raw)
}

// Engine runs the auto-switch decision loop. It is single-goroutine; the
// only cross-tick memory besides the state file is the unhealthy counter.
type Engine struct {
	Config
	Sink   EventSink
	Client *http.Client // nil means http.DefaultClient

	unhealthy int           // consecutive ticks with unknown active headroom
	wakeCh    chan struct{} // Wake() requests an immediate tick
	models    []string      // Config.Models with sentinels resolved, per tick
	// activeCause is why this tick's active headroom is unknown ("" when
	// known), set by emitPoll so the no-switch line can say it too.
	activeCause string
	burn        burnTracker // the active account's recent measurements
}

// NewEngine builds an Engine over cfg and sink, emitting a config-warning
// for every out-of-range value it has to correct.
func NewEngine(cfg Config, sink EventSink) *Engine {
	e := &Engine{Config: cfg, Sink: sink, wakeCh: make(chan struct{}, 1)}
	e.normalize()
	return e
}

// Wake cuts the loop's current sleep short so the next tick runs
// immediately. Safe from any goroutine; coalesces concurrent requests.
func (e *Engine) Wake() {
	select {
	case e.wakeCh <- struct{}{}:
	default:
	}
}

// normalize clamps out-of-range overrides back into the settings registry's
// bounds, warning about each correction.
func (e *Engine) normalize() {
	clamp := func(name string, v *float64, lo, hi float64) {
		if *v < lo || *v > hi {
			old := *v
			*v = math.Min(math.Max(*v, lo), hi)
			e.configWarning(fmt.Sprintf("%s %s out of range, using %s", name, formatNum(old), formatNum(*v)))
		}
	}
	clamp("threshold", &e.Threshold, 50, 99.9)
	clamp("interval", &e.Interval, 15, 3600)
	clamp("cooldown", &e.Cooldown, 0, 86400)
	clamp("hysteresis", &e.Hysteresis, 0, 50)
	if e.UnhealthyTicks < 1 || e.UnhealthyTicks > 100 {
		old := e.UnhealthyTicks
		e.UnhealthyTicks = int(math.Min(math.Max(float64(old), 1), 100))
		e.configWarning(fmt.Sprintf("unhealthyTicks %d out of range, using %d", old, e.UnhealthyTicks))
	}
	if e.Strategy != strategyBest && e.Strategy != strategyConsumeFirst && e.Strategy != strategyBalance {
		e.configWarning(fmt.Sprintf("unknown strategy %q, using %q", e.Strategy, strategyBest))
		e.Strategy = strategyBest
	}
}

// candidate is one non-active slot under consideration.
type candidate struct {
	slot     int
	acct     *account.Account
	headroom *float64 // nil = unknown
	usage    *usage.Usage
	reset    int64 // 7d reset epoch, filled for consume-first ranking
}

// tickResult is what one decision cycle tells the runner about pacing.
type tickResult struct {
	outcome Outcome
	// recoverAt is the earliest recovery epoch when the outcome is Blocked
	// because every account is exhausted (0 otherwise); the runner sleeps
	// until just past it.
	recoverAt int64
	// keepPolling marks a Blocked tick whose active account still has
	// headroom: the runner keeps the normal interval instead of backing off.
	// Past the threshold, backing off lets the active account run to 100%
	// unwatched; below it, backing off delays the threshold check and the
	// burn-rate projection. See blockedPacing.
	keepPolling bool
}

func result(o Outcome) tickResult { return tickResult{outcome: o} }

// tick runs one decision cycle.
func (e *Engine) tick() tickResult {
	// Re-resolve "auto" every tick: the model Claude Code uses (and with it
	// which per-model weekly window binds, e.g. Fable's) can change mid-run.
	e.models = settings.ResolveModelNames(e.Models)
	if err := paths.EnsureDirs(); err != nil {
		e.errorEvent("preparing backup dirs: "+err.Error(), false)
		return result(OutcomeError)
	}
	seq, err := account.Load()
	if err != nil {
		e.errorEvent(err.Error(), false)
		return result(OutcomeError)
	}
	e.releaseStaleQuarantines(seq)
	st := loadState()

	coll := &switcher.Collector{Client: e.Client, Models: e.models, Threshold: e.Threshold}
	snaps := coll.Collect(seq)

	head := make(map[string]*float64, len(snaps))
	var active *switcher.Snapshot
	for i := range snaps {
		s := &snaps[i]
		head[strconv.Itoa(s.Slot)] = nil
		if s.Usage != nil {
			if h, ok := s.Usage.Headroom(e.models); ok {
				head[strconv.Itoa(s.Slot)] = &h
			}
		}
		if s.Active && active == nil {
			active = s
		}
	}

	e.emitPoll(active, snaps, head)
	if active == nil {
		e.noSwitch("no-active-account", "")
		return result(OutcomeNoAction)
	}
	if active.Status == switcher.StatusAPIKey && !e.IncludeAPIKey {
		e.noSwitch("active-api-key", "")
		return result(OutcomeNoAction)
	}

	activeH := head[strconv.Itoa(active.Slot)]
	trigger, out, done := e.decideTrigger(activeH, e.projectCrossing(active, activeH, time.Now()))
	if done {
		return result(out)
	}

	if proactiveLike(trigger) {
		if rem := e.cooldownRemaining(st, time.Now()); rem > 0 {
			e.noSwitch("cooldown", fmt.Sprintf("%ds remaining", int(math.Ceil(rem))))
			return result(OutcomeNoAction)
		}
	}

	oauthCands, apiCands := e.candidates(snaps, st, active.Slot, head)
	if len(oauthCands)+len(apiCands) == 0 {
		return e.noCandidates(trigger)
	}

	trigger, ranked := e.selectTargets(trigger, oauthCands, apiCands, activeH, active)
	if len(ranked) == 0 {
		return e.nothingRanked(trigger, active, activeH, oauthCands, time.Now())
	}
	return result(e.performSwitch(trigger, active, activeH, ranked))
}

// noCandidates reports a tick with no switchable account besides the active
// one.
func (e *Engine) noCandidates(trigger string) tickResult {
	e.noSwitch("no-candidates", "")
	return blockedPacing(trigger, result(OutcomeBlocked))
}

// nothingRanked classifies a tick whose ranking came back empty. For a
// balance rebalance that is the steady state, not a block.
func (e *Engine) nothingRanked(
	trigger string, active *switcher.Snapshot, activeH *float64, cands []candidate, now time.Time,
) tickResult {
	if trigger == triggerBalance {
		return e.balanced(active, now)
	}
	return blockedPacing(trigger, e.blockedOutcome(active, activeH, cands))
}

// blockedPacing decides how long the runner waits after a Blocked tick, from
// what the trigger says about the active account. Only an account that
// cannot be worked on (exhausted or unmeasurable) earns a backoff:
//
//   - Below the threshold (consume-first, balance, projected) the active
//     account is healthy and the tick is discretionary. Under `best` such a
//     tick never even looks for candidates and keeps the interval; finding
//     none under another strategy must not change that, or the threshold
//     check and the burn-rate projection wait up to 5 minutes (or, when the
//     other accounts are all exhausted, until their recovery).
//   - Past the threshold (proactive) the account is still usable and burning
//     toward 100%, so the tick keeps polling — unless every candidate is
//     exhausted with a known recovery, where nothing can change before that
//     instant and the recovery wait applies.
//   - at-limit and failover keep the backoff and the recovery wait.
func blockedPacing(trigger string, r tickResult) tickResult {
	if r.outcome != OutcomeBlocked {
		return r
	}
	switch trigger {
	case triggerConsumeFirst, triggerBalance, triggerProjected:
		r.keepPolling = true
	case triggerProactive:
		r.keepPolling = r.recoverAt == 0
	}
	return r
}

// selectTargets orders the switch targets for this tick, possibly
// re-classifying the trigger. An empty result means the tick is blocked.
func (e *Engine) selectTargets(
	trigger string, oauthCands, apiCands []candidate, activeH *float64, active *switcher.Snapshot,
) (string, []candidate) {
	ranked := e.rank(trigger, oauthCands, activeH, active)
	if len(ranked) == 0 && trigger != triggerConsumeFirst && trigger != triggerBalance {
		// API-key accounts have no usage windows to rank; they are the
		// fallback when no measurable OAuth account qualifies.
		ranked = apiCands
	}
	if len(ranked) == 0 && trigger == triggerProactive {
		if lb := e.leastBad(oauthCands, activeH); len(lb) > 0 {
			return triggerLeastBad, lb
		}
	}
	return trigger, ranked
}

// proactiveLike triggers are discretionary — the active account still
// works — so they honor the cooldown.
func proactiveLike(trigger string) bool {
	return trigger == triggerProactive || trigger == triggerConsumeFirst ||
		trigger == triggerLeastBad || trigger == triggerProjected || trigger == triggerBalance
}

// decideTrigger classifies the active account. projected reports that the
// burn rate carries a below-threshold account past the threshold before the
// next fresh measurement. done=true means the tick is finished with the
// returned Outcome and no switch is attempted.
func (e *Engine) decideTrigger(activeH *float64, projected bool) (trigger string, out Outcome, done bool) {
	if activeH == nil {
		e.unhealthy++
		if e.unhealthy < e.UnhealthyTicks {
			detail := fmt.Sprintf("%d/%d before failover", e.unhealthy, e.UnhealthyTicks)
			if e.activeCause != "" {
				detail += "; " + e.activeCause
			}
			e.noSwitch("active-usage-unknown", detail)
			return "", OutcomeNoAction, true
		}
		return triggerFailover, 0, false
	}
	e.unhealthy = 0
	util := 100 - *activeH
	if util < e.Threshold {
		if projected {
			return triggerProjected, 0, false
		}
		switch e.Strategy {
		case strategyConsumeFirst:
			return triggerConsumeFirst, 0, false
		case strategyBalance:
			return triggerBalance, 0, false
		}
		e.noSwitch("below-threshold", fmt.Sprintf("%s%% < %s%%", formatNum(util), formatNum(e.Threshold)))
		return "", OutcomeNoAction, true
	}
	if *activeH <= 0 {
		return triggerAtLimit, 0, false
	}
	return triggerProactive, 0, false
}

// candidates partitions the non-active slots into OAuth candidates and the
// API-key fallback list, dropping disabled, quarantined, un-switchable, and
// credential-dead slots.
func (e *Engine) candidates(
	snaps []switcher.Snapshot, st *state, activeSlot int, head map[string]*float64,
) (oauthCands, apiCands []candidate) {
	for i := range snaps {
		s := &snaps[i]
		a := s.Account
		if s.Slot == activeSlot || a == nil || a.Disabled {
			continue
		}
		if _, q := st.Quarantine[strconv.Itoa(s.Slot)]; q {
			continue
		}
		switch s.Status {
		case switcher.StatusReloginRequired, switcher.StatusNoCredentials, switcher.StatusKeychainUnavailable:
			continue
		}
		if !switcher.Switchable(s.Slot, a) {
			continue
		}
		c := candidate{slot: s.Slot, acct: a, headroom: head[strconv.Itoa(s.Slot)], usage: s.Usage}
		if s.Status == switcher.StatusAPIKey {
			if e.IncludeAPIKey {
				apiCands = append(apiCands, c)
			}
			continue
		}
		oauthCands = append(oauthCands, c)
	}
	sort.SliceStable(apiCands, func(i, j int) bool { return apiCands[i].slot < apiCands[j].slot })
	return oauthCands, apiCands
}

// rank filters and orders the OAuth candidates for the trigger. Unknown
// headroom and exhausted candidates never rank; proactive-like triggers also
// require the target to land healthy (below threshold).
func (e *Engine) rank(trigger string, cands []candidate, activeH *float64, active *switcher.Snapshot) []candidate {
	var out []candidate
	if trigger == triggerAtLimit || trigger == triggerFailover {
		for _, c := range cands {
			if c.headroom != nil && *c.headroom > 0 {
				out = append(out, c)
			}
		}
		sortByHeadroomDesc(out)
		return out
	}

	// proactive / consume-first / balance: the target must be healthy.
	if e.Strategy == strategyBalance {
		return e.rankBalance(trigger, cands, activeH, active, time.Now())
	}
	if e.Strategy == strategyConsumeFirst {
		activeReset := sevenDayReset(active.Usage)
		if activeReset == 0 {
			return nil
		}
		for _, c := range cands {
			if c.headroom == nil || *c.headroom <= 0 || 100-*c.headroom >= e.Threshold {
				continue
			}
			c.reset = sevenDayReset(c.usage)
			if c.reset == 0 || c.reset >= activeReset {
				continue
			}
			out = append(out, c)
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].reset != out[j].reset {
				return out[i].reset < out[j].reset
			}
			return out[i].slot < out[j].slot
		})
		return out
	}

	for _, c := range cands {
		if c.headroom == nil || *c.headroom <= 0 || 100-*c.headroom >= e.Threshold {
			continue
		}
		if activeH != nil && *c.headroom-*activeH < e.Hysteresis {
			continue
		}
		out = append(out, c)
	}
	sortByHeadroomDesc(out)
	return out
}

// leastBad ranks the fallback targets once the active account is past the
// threshold and nothing healthy qualifies: any measured, non-exhausted
// candidate with clearly more headroom than the active account, best first.
//
// "Clearly" is the hysteresis margin, shrunk to half the active account's
// remaining headroom. At full hysteresis the path could never fire before
// the active account hit 100% (a candidate would need headroom of at least
// threshold-room + hysteresis, which already qualifies it as healthy); the
// shrinking margin makes the move more eager the closer the wall is. It
// still cannot ping-pong: moving back needs the new account to burn twice
// the margin first, and the cooldown applies.
func (e *Engine) leastBad(cands []candidate, activeH *float64) []candidate {
	if activeH == nil || *activeH <= 0 {
		return nil
	}
	margin := math.Min(e.Hysteresis, *activeH/2)
	var out []candidate
	for _, c := range cands {
		if c.headroom == nil || *c.headroom <= 0 {
			continue
		}
		gain := *c.headroom - *activeH
		if gain <= 0 || gain < margin {
			continue
		}
		out = append(out, c)
	}
	sortByHeadroomDesc(out)
	return out
}

func sortByHeadroomDesc(cands []candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		if *cands[i].headroom != *cands[j].headroom {
			return *cands[i].headroom > *cands[j].headroom
		}
		return cands[i].slot < cands[j].slot
	})
}

// sevenDayReset returns the 7d window's reset epoch, or 0 when unknown.
func sevenDayReset(u *usage.Usage) int64 {
	if u == nil || u.SevenDay == nil {
		return 0
	}
	return usage.ParseReset(u.SevenDay.ResetsAt)
}

// blockedOutcome classifies an empty ranking: everything exhausted (with a
// recovery hint for the runner), nothing measurable, or nothing qualifying.
// How long the runner then waits is blockedPacing's call.
func (e *Engine) blockedOutcome(active *switcher.Snapshot, activeH *float64, cands []candidate) tickResult {
	measured, exhausted := 0, 0
	for _, c := range cands {
		if c.headroom == nil {
			continue
		}
		measured++
		if *c.headroom <= 0 {
			exhausted++
		}
	}
	if measured > 0 && measured == exhausted {
		earliest := e.earliestRecovery(active, activeH, cands)
		fields := map[string]any{"earliestResetAt": nil}
		if earliest > 0 {
			fields["earliestResetAt"] = time.Unix(earliest, 0).UTC().Format(account.TimeFormat)
		}
		e.emit("all-exhausted", fields)
		return tickResult{outcome: OutcomeBlocked, recoverAt: earliest}
	}
	if measured == 0 {
		e.noSwitch("no-comparison", "")
		return result(OutcomeBlocked)
	}
	e.noSwitch("no-qualifying-candidate", "")
	return result(OutcomeBlocked)
}

// earliestRecovery is the soonest instant any exhausted account (active
// included) recovers fully, or 0 when no reset is known.
func (e *Engine) earliestRecovery(active *switcher.Snapshot, activeH *float64, cands []candidate) int64 {
	var best int64
	consider := func(u *usage.Usage, h *float64) {
		if u == nil || h == nil || *h > 0 {
			return
		}
		if r := u.LatestExhaustedReset(e.models); r > 0 && (best == 0 || r < best) {
			best = r
		}
	}
	consider(active.Usage, activeH)
	for _, c := range cands {
		consider(c.usage, c.headroom)
	}
	return best
}

// performSwitch activates the best ranked candidate, walking down the list
// past candidates whose credential cannot be freshened.
func (e *Engine) performSwitch(
	trigger string,
	active *switcher.Snapshot,
	activeH *float64,
	ranked []candidate,
) Outcome {
	if e.DryRun {
		c := ranked[0]
		e.emit("switch", map[string]any{
			"trigger": trigger, "from": active.Slot, "to": c.slot,
			"toEmail": c.acct.Email, "warnings": []string{}, "dryRun": true,
		})
		return OutcomeSwitched
	}
	for _, c := range ranked {
		if !e.freshen(c) {
			continue
		}
		return e.commitSwitch(trigger, active, activeH, c)
	}
	e.errorEvent("no ranked candidate could be activated (credential refresh failures)", true)
	return OutcomeError
}

// freshen prepares a candidate's stored credential for activation: an OAuth
// token expiring within the 10-minute margin is refreshed in place. A
// permanent refresh failure quarantines the slot; any failure reports the
// candidate as unusable so the next ranked one is tried.
func (e *Engine) freshen(c candidate) bool {
	cred, unreadable := credentials.ReadBackup(c.slot, c.acct.Email)
	if unreadable || cred == "" {
		return false
	}
	if credentials.IsAPIKey(cred) {
		return true
	}
	blob, err := oauth.ParseBlob([]byte(cred))
	if err != nil || blob.OAuth == nil {
		// Malformed blob: let SwitchTo produce the authoritative error.
		return true
	}
	if !oauth.Expired(blob.OAuth.ExpiresAt, time.Now().Add(freshenLead)) {
		return true
	}
	outcome := oauth.Refresh(e.client(), []byte(cred), time.Now)
	switch {
	case outcome.Err == oauth.ErrNone:
		if raw, merr := oauth.MarshalBlob(outcome.Blob); merr == nil {
			if werr := credentials.WriteBackup(c.slot, c.acct.Email, string(raw)); werr != nil {
				e.errorEvent(fmt.Sprintf("writing refreshed backup for Account-%d: %v", c.slot, werr), true)
			}
		}
		return true
	case outcome.Err.Permanent():
		e.quarantineSlot(c, string(outcome.Err), cred)
		return false
	default:
		return false // transient: try the next ranked candidate
	}
}

// commitSwitch re-checks the cooldown under the state lock, performs the
// switch, and records the outgoing account's parting state.
func (e *Engine) commitSwitch(trigger string, active *switcher.Snapshot, activeH *float64, c candidate) Outcome {
	lock := locks.NewFileLock(stateLockPath())
	if err := lock.Acquire(); err != nil {
		e.errorEvent("acquiring autoswitch state lock: "+err.Error(), true)
		return OutcomeError
	}
	defer lock.Release()

	st := loadState()
	now := time.Now()
	if proactiveLike(trigger) {
		if rem := e.cooldownRemaining(st, now); rem > 0 {
			e.noSwitch("cooldown", fmt.Sprintf("%ds remaining", int(math.Ceil(rem))))
			return OutcomeNoAction
		}
	}

	res, err := switcher.SwitchToFrom(strconv.Itoa(c.slot), false, e.switchOrigin(trigger, activeH))
	if err != nil {
		e.errorEvent("switch failed: "+err.Error(), true)
		return OutcomeError
	}
	if !res.Switched {
		e.noSwitch(res.Reason, "")
		return OutcomeNoAction
	}

	st.LastSwitchAt = float64(now.UnixNano()) / 1e9
	st.LastSwitchTo = strconv.Itoa(c.slot)
	st.LastSwitchFrom = active.Slot
	st.LeftHeadroom = activeH
	st.LeftRecoveryAt = nil
	if active.Usage != nil {
		if r := active.Usage.EarliestFutureReset(e.models, now); r > 0 {
			f := float64(r)
			st.LeftRecoveryAt = &f
		}
	}
	st.LeftTrigger = trigger
	if serr := saveState(st); serr != nil {
		e.errorEvent("persisting autoswitch state: "+serr.Error(), true)
	}
	e.unhealthy = 0

	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	e.emit("switch", map[string]any{
		"trigger": trigger, "from": active.Slot, "to": c.slot,
		"toEmail": c.acct.Email, "warnings": warnings, "dryRun": false,
	})
	return OutcomeSwitched
}

// switchOrigin describes an auto switch for the history record.
func (e *Engine) switchOrigin(trigger string, activeH *float64) switcher.Origin {
	o := switcher.Origin{Trigger: trigger, Source: "auto"}
	if activeH == nil {
		o.Reason = "active account usage unknown"
		return o
	}
	util := 100 - *activeH
	o.ActiveUtilizationPct = &util
	switch trigger {
	case triggerAtLimit:
		o.Reason = "active account at its limit"
	case triggerConsumeFirst:
		o.Reason = "consume-first: target resets sooner"
	case triggerBalance:
		o.Reason = "balance: target has more weekly headroom per hour to its reset"
	default:
		o.Reason = fmt.Sprintf("active at %s%% (threshold %s%%)", formatNum(util), formatNum(e.Threshold))
	}
	return o
}

// cooldownRemaining returns the seconds left in the proactive cooldown, 0
// when it has elapsed (or never started).
func (e *Engine) cooldownRemaining(st *state, now time.Time) float64 {
	if st.LastSwitchAt == 0 {
		return 0
	}
	rem := e.Cooldown - (float64(now.UnixNano())/1e9 - st.LastSwitchAt)
	if rem < 0 {
		return 0
	}
	return rem
}

// quarantineSlot records a dead refresh lineage so the slot is skipped until
// its credential (or the account) is replaced.
func (e *Engine) quarantineSlot(c candidate, reason, cred string) {
	err := mutateState(func(st *state) bool {
		st.Quarantine[strconv.Itoa(c.slot)] = &quarantineRecord{
			Email:                   c.acct.Email,
			Reason:                  reason,
			At:                      time.Now().UTC().Format(account.TimeFormat),
			RefreshTokenFingerprint: oauth.Fingerprint([]byte(cred)),
		}
		return true
	})
	if err != nil {
		e.errorEvent(fmt.Sprintf("quarantining Account-%d: %v", c.slot, err), true)
	}
	e.emit("account-quarantined", map[string]any{"number": c.slot, "email": c.acct.Email, "reason": reason})
}

// releaseStaleQuarantines drops quarantine rows whose slot was removed or
// re-added ("account-replaced") or whose stored credential fingerprint no
// longer matches ("credentials-replaced"). Dry-run never mutates state.
func (e *Engine) releaseStaleQuarantines(seq *account.Sequence) {
	if e.DryRun {
		return
	}
	type release struct {
		slot          int
		email, reason string
	}
	var released []release
	err := mutateState(func(st *state) bool {
		for key, q := range st.Quarantine {
			slot, aerr := strconv.Atoi(key)
			var a *account.Account
			if aerr == nil {
				a = seq.Get(slot)
			}
			if a == nil || a.Email != q.Email {
				delete(st.Quarantine, key)
				released = append(released, release{slot, q.Email, "account-replaced"})
				continue
			}
			cred, unreadable := credentials.ReadBackup(slot, a.Email)
			if !unreadable && cred != "" && oauth.Fingerprint([]byte(cred)) != q.RefreshTokenFingerprint {
				delete(st.Quarantine, key)
				released = append(released, release{slot, q.Email, "credentials-replaced"})
			}
		}
		return len(released) > 0
	})
	if err != nil {
		e.errorEvent("releasing quarantines: "+err.Error(), true)
		return
	}
	for _, r := range released {
		e.emit("account-unquarantined", map[string]any{"number": r.slot, "email": r.email, "reason": r.reason})
	}
}

func (e *Engine) emitPoll(active *switcher.Snapshot, snaps []switcher.Snapshot, head map[string]*float64) {
	fields := map[string]any{"threshold": e.Threshold, "headroomPct": head}
	if active != nil {
		fields["active"] = map[string]any{"number": active.Slot, "email": active.Account.Email}
	} else {
		fields["active"] = nil
	}
	fetchErrors := map[string]string{}
	unknown := map[string]string{}
	windows := map[string]map[string]float64{}
	e.activeCause = ""
	for i := range snaps {
		s := &snaps[i]
		key := strconv.Itoa(s.Slot)
		if head[key] == nil {
			if cause := unknownCause(s, e.models); cause != "" {
				unknown[key] = cause
				if s == active {
					e.activeCause = cause
				}
			}
		}
		if s.Usage != nil {
			wm := map[string]float64{}
			for _, w := range s.Usage.RelevantWindows(e.models) {
				wm[w.Name] = w.Pct
			}
			if len(wm) > 0 {
				windows[key] = wm
			}
		} else if s.LastErr != "" {
			fetchErrors[key] = s.LastErr
		}
	}
	if len(fetchErrors) > 0 {
		fields["fetchErrors"] = fetchErrors
	}
	if len(unknown) > 0 {
		fields["usageUnknown"] = unknown
	}
	if len(windows) > 0 {
		fields["windowsPct"] = windows
	}
	e.emit("poll", fields)
}

func (e *Engine) emit(kind string, fields map[string]any) {
	if e.Sink == nil {
		return
	}
	e.Sink.Emit(Event{Kind: kind, TS: time.Now(), Fields: fields})
}

func (e *Engine) noSwitch(reason, detail string) {
	f := map[string]any{"reason": reason}
	if detail != "" {
		f["detail"] = detail
	}
	e.emit("no-switch", f)
}

func (e *Engine) errorEvent(message string, transient bool) {
	e.emit("error", map[string]any{"message": message, "transient": transient})
}

func (e *Engine) configWarning(message string) {
	e.emit("config-warning", map[string]any{"message": message})
}

func (e *Engine) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return http.DefaultClient
}
