package autoswitch

import (
	"testing"

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
