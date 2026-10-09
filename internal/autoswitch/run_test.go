package autoswitch

import (
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/locks"
)

func TestEngineRunningProbe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")

	if EngineRunning() {
		t.Fatal("no engine is running; probe must be false")
	}

	// Simulate a running loop by holding the marker.
	marker := locks.NewFileLock(engineLockPath())
	ok, err := marker.TryAcquire()
	if err != nil || !ok {
		t.Fatalf("TryAcquire: %v %v", ok, err)
	}
	defer marker.Release()

	if !EngineRunning() {
		t.Fatal("marker held; probe must report a running engine")
	}

	marker.Release()
	if EngineRunning() {
		t.Fatal("marker released; probe must be false again")
	}
}

// All-exhausted with a known recovery: sleep until just past it, however
// far off (#18 logged the same recovery every 10 minutes for hours), bounded
// by an hour so a wrong reset time still self-corrects.
func TestDelayAfterAllExhaustedWaitsForRecovery(t *testing.T) {
	e := NewEngine(Config{
		Threshold: 90, Interval: 60, Hysteresis: 10, Strategy: strategyBest, UnhealthyTicks: 3,
	}, nil)
	now := time.Unix(1_800_000_000, 0) // fake clock
	at := func(d time.Duration) tickResult {
		return tickResult{outcome: OutcomeBlocked, recoverAt: now.Add(d).Unix()}
	}
	cases := []struct {
		name string
		res  tickResult
		want float64
	}{
		{"recovery in 40m", at(40 * time.Minute), 40*60 + recoveryMargin},
		{"recovery in 15m", at(15 * time.Minute), 15*60 + recoveryMargin},
		{"recovery in 3h is capped", at(3 * time.Hour), maxRecoveryWait},
		{"recovery imminent floors at the interval", at(-5 * time.Minute), 60},
		{"recovery unknown backs off", tickResult{outcome: OutcomeBlocked}, 300},
	}
	for _, c := range cases {
		if got := e.delayAfter(c.res, now); got != c.want {
			t.Errorf("%s: delay = %vs, want %vs", c.name, got, c.want)
		}
	}
}
