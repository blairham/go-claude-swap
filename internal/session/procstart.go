package session

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// pidReuseSlack absorbs the small clock steps that move a `ps` start time on
// some platforms. Linux's tick stamp needs none.
const pidReuseSlack = 120 * time.Second

// pidMatchesRecord reports whether the live process at pid is the one that
// wrote a record stamped procStart, Claude Code's reading of its own start.
//
// On Linux the stamp is /proc/<pid>/stat's start time in clock ticks since
// boot, fixed for the process's life and never shared by two processes at
// one pid, so equality is the whole test. Elsewhere it is `ps -o lstart`, a
// wall-clock time: a recycled pid belongs to a process that started after
// the recorded one, and only that direction disqualifies — and only a
// process that is not a claude, since some `ps` builds move every start
// time with each wall-clock step. Everything unknowable (no stamp, an
// unparseable one, no ps, Windows) passes: "cannot tell" must never turn a
// live session into "nobody there", because the callers gate destructive
// steps.
func pidMatchesRecord(pid int, procStart string) bool {
	if procStart == "" {
		return true
	}
	if isDigits(procStart) {
		ticks, ok := processStartTicks(pid)
		return !ok || ticks == procStart
	}
	recorded, ok := parseLstart(procStart)
	if !ok {
		return true
	}
	started, ok := processStartedAt(pid)
	if !ok || started <= recorded+int64(pidReuseSlack/time.Second) {
		return true
	}
	isClaude, known := processIsClaude(pid)
	return !known || isClaude
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// processStartTicks is field 22 of /proc/<pid>/stat (Linux only by
// construction). A variable for tests.
var processStartTicks = func(pid int) (string, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	return statStartTicks(string(raw))
}

// statStartTicks extracts the start time from a /proc/<pid>/stat line. The
// command name sits in parentheses and may itself hold spaces or
// parentheses, so fields are counted from the last ')'.
func statStartTicks(line string) (string, bool) {
	i := strings.LastIndexByte(line, ')')
	if i < 0 {
		return "", false
	}
	fields := strings.Fields(line[i+1:])
	if len(fields) <= 19 || !isDigits(fields[19]) {
		return "", false
	}
	return fields[19], true
}

// ps runs `ps -o <columns> -p <pid>` the way Claude Code reads its own
// start (LC_ALL=C, TZ=UTC). ok is false on Windows and on any failure.
func ps(pid int, columns ...string) (string, bool) {
	if runtime.GOOS == "windows" {
		return "", false
	}
	cols := make([]string, len(columns))
	for i, c := range columns {
		cols[i] = c + "="
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", strings.Join(cols, ","), "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	text := strings.TrimSpace(string(out))
	if err != nil || text == "" {
		return "", false
	}
	return text, true
}

// processStartedAt is when pid started, in epoch seconds, read as Claude
// Code stamps procStart. A variable for tests.
var processStartedAt = func(pid int) (int64, bool) {
	text, ok := ps(pid, "lstart")
	if !ok {
		return 0, false
	}
	return parseLstart(text)
}

// processIsClaude judges from `ps -o comm=,args=` whether pid looks like a
// Claude Code (the native binary is named claude; an npm install runs
// cli.js from a claude-code package). known is false when ps cannot tell.
// A variable for tests.
var processIsClaude = func(pid int) (isClaude, known bool) {
	text, ok := ps(pid, "comm", "args")
	if !ok {
		return false, false
	}
	return strings.Contains(strings.ToLower(text), "claude"), true
}

var months = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// parseLstart reads `ps -o lstart` under LC_ALL=C TZ=UTC
// ("Wed Sep  2 20:35:59 2026") as epoch seconds. By hand, so no locale or
// layout padding can get in the way.
func parseLstart(text string) (int64, bool) {
	parts := strings.Fields(text)
	if len(parts) != 5 {
		return 0, false
	}
	month := slices.Index(months, parts[1])
	if month < 0 {
		return 0, false
	}
	day, derr := strconv.Atoi(parts[2])
	year, yerr := strconv.Atoi(parts[4])
	clock := strings.Split(parts[3], ":")
	if derr != nil || yerr != nil || len(clock) != 3 {
		return 0, false
	}
	var hms [3]int
	for i, c := range clock {
		v, err := strconv.Atoi(c)
		if err != nil {
			return 0, false
		}
		hms[i] = v
	}
	return time.Date(year, time.Month(month+1), day, hms[0], hms[1], hms[2], 0, time.UTC).Unix(), true
}
