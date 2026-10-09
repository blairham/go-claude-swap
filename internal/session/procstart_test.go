package session

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestParseLstartMatchesOriginal(t *testing.T) {
	// calendar.timegm((2026, 9, 2, 20, 35, 59, 0, 0, 0)) in claude-swap.
	if got, ok := parseLstart("Wed Sep  2 20:35:59 2026"); !ok || got != 1788381359 {
		t.Fatalf("parseLstart = %d, %v", got, ok)
	}
	for _, bad := range []string{"", "Wed Foo  2 20:35:59 2026", "Wed Sep 2 20:35 2026", "1234"} {
		if _, ok := parseLstart(bad); ok {
			t.Errorf("parseLstart(%q) accepted", bad)
		}
	}
}

func TestStatStartTicks(t *testing.T) {
	// The command name may hold spaces and parentheses.
	line := "42 (we(ird) name) S 1 42 42 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 10"
	if got, ok := statStartTicks(line); !ok || got != "987654" {
		t.Fatalf("statStartTicks = %q, %v", got, ok)
	}
	if _, ok := statStartTicks("42 (x) S 1"); ok {
		t.Fatal("short stat line accepted")
	}
}

// stubProc replaces the process probes for one test.
func stubProc(t *testing.T, ticks string, started int64, isClaude, known bool) {
	t.Helper()
	pt, pa, pc := processStartTicks, processStartedAt, processIsClaude
	processStartTicks = func(int) (string, bool) { return ticks, ticks != "" }
	processStartedAt = func(int) (int64, bool) { return started, started != 0 }
	processIsClaude = func(int) (bool, bool) { return isClaude, known }
	t.Cleanup(func() { processStartTicks, processStartedAt, processIsClaude = pt, pa, pc })
}

func TestPIDMatchesRecord(t *testing.T) {
	const stamp = "Wed Sep  2 20:35:59 2026"
	const recorded = 1788381359
	cases := []struct {
		name      string
		procStart string
		ticks     string
		started   int64
		isClaude  bool
		known     bool
		want      bool
	}{
		{"unstamped", "", "", 0, false, true, true},
		{"ticks equal", "555", "555", 0, false, true, true},
		{"ticks differ: recycled", "555", "999", 0, false, true, false},
		{"ticks unknowable", "555", "", 0, false, true, true},
		{"unparseable stamp", "yesterday", "", 0, false, true, true},
		{"started before the record", stamp, "", recorded - 50, false, true, true},
		{"within the slack", stamp, "", recorded + 100, false, true, true},
		{"started later, a stranger: recycled", stamp, "", recorded + 1000, false, true, false},
		{"started later, still a claude", stamp, "", recorded + 1000, true, true, true},
		{"started later, cannot tell", stamp, "", recorded + 1000, false, false, true},
		{"start unknowable", stamp, "", 0, false, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubProc(t, c.ticks, c.started, c.isClaude, c.known)
			if got := pidMatchesRecord(os.Getpid(), c.procStart); got != c.want {
				t.Fatalf("pidMatchesRecord = %v, want %v", got, c.want)
			}
		})
	}
}

// TestScanLiveSkipsRecycledPID: a record whose pid now belongs to another
// process is not a live session; a procStart of the wrong type makes the
// record unreadable, as it does for claude-swap.
func TestScanLiveSkipsRecycledPID(t *testing.T) {
	env(t)
	dir := Dir(1, "a@b.co")
	sessions := filepath.Join(dir, "sessions")
	os.MkdirAll(sessions, 0o700)
	write := func(name, procStart string) {
		os.WriteFile(filepath.Join(sessions, name), fmt.Appendf(nil, `{"pid":%d,"procStart":%s}`, os.Getpid(), procStart), 0o600)
	}
	stubProc(t, "999", 0, false, true)

	write("a.json", `"555"`)
	if l := ScanLive(dir); l.Busy() {
		t.Fatalf("recycled pid counted live: %+v", l)
	}
	write("a.json", `"999"`)
	if l := ScanLive(dir); len(l.PIDs) != 1 {
		t.Fatalf("matching record not live: %+v", l)
	}
	write("a.json", `555`)
	if l := ScanLive(dir); l.Unreadable != 1 {
		t.Fatalf("non-string procStart not unreadable: %+v", l)
	}
}

// TestProcessProbesReadThisProcess proves the real probes can answer: this
// test process started moments ago, and on Linux has a tick stamp.
func TestProcessProbesReadThisProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no ps")
	}
	started, ok := processStartedAt(os.Getpid())
	if !ok {
		t.Fatal("ps lstart unreadable for this process")
	}
	if age := time.Since(time.Unix(started, 0)); age < -time.Minute || age > time.Hour {
		t.Fatalf("this process started %v ago?", age)
	}
	if _, known := processIsClaude(os.Getpid()); !known {
		t.Fatal("ps comm,args unreadable for this process")
	}
	if runtime.GOOS == "linux" {
		if _, ok := processStartTicks(os.Getpid()); !ok {
			t.Fatal("/proc stat start ticks unreadable")
		}
	}
}
