package autoswitch

import (
	"context"
	"math"
	"math/rand"
	"path/filepath"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// engineLockPath is the presence marker a looping engine holds so other
// cswap processes (the TUI, list) can tell an engine is already fetching
// usage and stay store-only, keeping the shared request budget single-owner.
func engineLockPath() string {
	return filepath.Join(paths.BackupRoot(), ".engine-running.lock")
}

// EngineRunning reports whether a cswap auto loop is active on this machine
// (its presence lock is held). A probe, not a lock: callers use it to choose
// a store-only read path.
func EngineRunning() bool {
	l := locks.NewFileLock(engineLockPath())
	ok, err := l.TryAcquire()
	if err != nil {
		return false
	}
	if ok {
		l.Release()
		return false
	}
	return true
}

// Run ticks forever until ctx is canceled, then returns nil. Normal ticks
// are spaced by the configured interval with ±10% jitter; a Blocked tick
// with a known recovery instant sleeps until just past that reset (never
// beyond 10 minutes), and any other Blocked tick backs off to at least 5
// minutes. A sleep event is emitted whenever the delay exceeds 1.5×interval.
func (e *Engine) Run(ctx context.Context) error {
	// Hold the presence marker for the loop's lifetime so TUIs go
	// store-only instead of double-spending the usage request budget.
	// Best-effort: if another engine already holds it, both still behave
	// correctly through the shared store.
	marker := locks.NewFileLock(engineLockPath())
	if ok, err := marker.TryAcquire(); err == nil && ok {
		defer marker.Release()
	}
	for {
		outcome, blockedReset := e.tick()
		now := time.Now()
		delay := e.delayAfter(outcome, blockedReset, now)
		if delay > 1.5*e.Interval {
			until := now.Add(time.Duration(delay * float64(time.Second)))
			e.emit("sleep", map[string]any{
				"seconds": math.Round(delay),
				"until":   until.UTC().Format(account.TimeFormat),
			})
		}
		timer := time.NewTimer(time.Duration(delay * float64(time.Second)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-e.wakeCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// RunOnce performs a single tick; the Outcome doubles as the `--once` exit
// code.
func (e *Engine) RunOnce() Outcome {
	outcome, _ := e.tick()
	return outcome
}

// delayAfter computes the next sleep in seconds.
func (e *Engine) delayAfter(outcome Outcome, blockedReset int64, now time.Time) float64 {
	if outcome == OutcomeBlocked {
		if blockedReset > 0 {
			d := float64(blockedReset+60) - float64(now.Unix())
			return math.Min(math.Max(d, e.Interval), 600)
		}
		return math.Max(e.Interval, 300)
	}
	return e.Interval * (0.9 + 0.2*rand.Float64()) //nolint:gosec // jitter, not crypto
}
