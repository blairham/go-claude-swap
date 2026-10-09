package switcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/session"
)

// probeAs stubs `claude auth status` to report a login as email whenever
// the profile holds a credential ("" reports a probe that did not answer).
func probeAs(t *testing.T, email string) *int {
	t.Helper()
	calls := 0
	prev := session.Probe
	session.Probe = func(dir string) (session.Verdict, session.Status) {
		calls++
		if email == "" {
			return session.Unknown, session.Status{}
		}
		// Like the real command: logged in only with a credential present.
		in := session.ReadCredentials(dir) != ""
		return session.Valid, session.Status{LoggedIn: &in, AuthMethod: "claude.ai", Email: email}
	}
	t.Cleanup(func() { session.Probe = prev })
	return &calls
}

func credExp(access, refresh string, exp int64) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"%s","refreshToken":"%s","expiresAt":%d}}`, access, refresh, exp)
}

// sessionAccounts leaves b@b.co as the default login and a@b.co in slot 1.
func sessionAccounts(t *testing.T) string {
	t.Helper()
	home := env(t)
	login(t, home, "a@b.co", "", credExp("at-a", "rt-a", 9999999999000), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	login(t, home, "b@b.co", "", credExp("at-b", "rt-b", 9999999999000), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	return home
}

func markLive(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "sessions"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "sessions", "x.json"), fmt.Appendf(nil, `{"pid": %d}`, os.Getpid()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareSessionBootstrapsThenReuses(t *testing.T) {
	sessionAccounts(t)
	probeAs(t, "a@b.co")

	plan, err := PrepareSession("a@b.co", SessionOptions{Share: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Direct || plan.Slot != 1 || plan.Dir != session.Dir(1, "a@b.co") {
		t.Fatalf("plan = %+v", plan)
	}
	backup, _ := credentials.ReadBackup(1, "a@b.co")
	if got := session.ReadCredentials(plan.Dir); got != backup {
		t.Fatalf("profile seeded with %q, backup is %q", got, backup)
	}
	// A valid profile is reused as-is: its rotated token is the newest.
	os.WriteFile(filepath.Join(plan.Dir, ".credentials.json"), []byte("rotated-in-session"), 0o600)
	if _, err := PrepareSession("1", SessionOptions{Share: true}); err != nil {
		t.Fatal(err)
	}
	if got := session.ReadCredentials(plan.Dir); got != "rotated-in-session" {
		t.Fatalf("valid profile was re-seeded: %q", got)
	}
}

func TestPrepareSessionDirectForDefaultLogin(t *testing.T) {
	sessionAccounts(t)
	calls := probeAs(t, "b@b.co")
	plan, err := PrepareSession("b@b.co", SessionOptions{})
	if err != nil || !plan.Direct {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if *calls != 0 {
		t.Fatal("fast path probed a profile")
	}
	if _, err := os.Stat(session.Dir(2, "b@b.co")); err == nil {
		t.Fatal("fast path created a second copy of the live credential")
	}
	if _, err := PrepareSession("b@b.co", SessionOptions{RequireSession: true}); err == nil {
		t.Fatal("--require-session took the fast path")
	}
	// With CLAUDE_CONFIG_DIR set there is no "default login" to compare to.
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	probeAs(t, "b@b.co")
	if plan, err := PrepareSession("b@b.co", SessionOptions{}); err != nil || plan.Direct {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
}

func TestPrepareSessionFailures(t *testing.T) {
	sessionAccounts(t)
	if _, err := AddToken("sk-ant-api03-k", "", 0); err != nil {
		t.Fatal(err)
	}
	probeAs(t, "a@b.co")
	if _, err := PrepareSession("3", SessionOptions{}); err == nil || !strings.Contains(err.Error(), "API-key") {
		t.Fatalf("API-key account: %v", err)
	}

	// A profile that is positively logged in as someone else is removed.
	probeAs(t, "someone@else.co")
	if _, err := PrepareSession("1", SessionOptions{}); err == nil {
		t.Fatal("invalid profile accepted")
	}
	if _, err := os.Stat(session.Dir(1, "a@b.co")); err == nil {
		t.Fatal("invalid profile left behind")
	}

	// A probe that did not answer is not evidence against a freshly seeded
	// profile: launch.
	probeAs(t, "")
	if _, err := PrepareSession("1", SessionOptions{}); err != nil {
		t.Fatalf("unanswered probe over a seeded profile: %v", err)
	}
}

// TestStaleProfileRebootstrapsOnceQuiescent: a backup written while the
// session was live flags it; the next run after it exits re-seeds.
func TestStaleProfileRebootstrapsOnceQuiescent(t *testing.T) {
	home := sessionAccounts(t)
	probeAs(t, "a@b.co")
	plan, err := PrepareSession("1", SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	markLive(t, plan.Dir)

	// Re-login of a@b.co while its session runs.
	login(t, home, "a@b.co", "", credExp("at-a2", "rt-a2", 9999999999000), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	login(t, home, "b@b.co", "", credExp("at-b", "rt-b", 9999999999000), nil)
	if !session.IsStale(plan.Dir) || !strings.Contains(session.ReadCredentials(plan.Dir), "rt-a\"") {
		t.Fatal("live profile not flagged stale, or rewritten under its session")
	}
	os.RemoveAll(filepath.Join(plan.Dir, "sessions"))
	if _, err := PrepareSession("1", SessionOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session.ReadCredentials(plan.Dir), "rt-a2") || session.IsStale(plan.Dir) {
		t.Fatalf("stale profile not re-seeded: %q", session.ReadCredentials(plan.Dir))
	}
}

// TestSwitchAdoptsNewerSessionCredential: Claude Code rotated the family in
// the profile; activating the backup's consumed generation would fail.
func TestSwitchAdoptsNewerSessionCredential(t *testing.T) {
	sessionAccounts(t)
	probeAs(t, "a@b.co")
	plan, err := PrepareSession("1", SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rotated := credExp("at-a-new", "rt-a-new", 9999999999999)
	os.WriteFile(filepath.Join(plan.Dir, ".credentials.json"), []byte(rotated), 0o600)

	// Live: refuse, the backup is a consumed generation.
	markLive(t, plan.Dir)
	if _, err := SwitchTo("1", false); err == nil || !strings.Contains(err.Error(), "rotated past") {
		t.Fatalf("switch under a live, rotated session: %v", err)
	}
	// Quiescent: adopt, then activate the newest generation.
	os.RemoveAll(filepath.Join(plan.Dir, "sessions"))
	if _, err := SwitchTo("1", false); err != nil {
		t.Fatal(err)
	}
	if got := liveOAuth(t)["refreshToken"]; got != "rt-a-new" {
		t.Fatalf("activated refresh token = %v", got)
	}
	// Adoption must not invalidate the profile it adopted from.
	if session.ReadCredentials(plan.Dir) != rotated {
		t.Fatal("adoption invalidated the session profile")
	}
}

func TestSessionCredForUsage(t *testing.T) {
	sessionAccounts(t)
	probeAs(t, "a@b.co")
	plan, _ := PrepareSession("1", SessionOptions{})
	rotated := credExp("at-a-new", "rt-a-new", 9999999999999)
	os.WriteFile(filepath.Join(plan.Dir, ".credentials.json"), []byte(rotated), 0o600)
	seq, _ := account.Load()

	markLive(t, plan.Dir)
	if cred, owned := sessionCredForUsage(1, seq.Get(1)); cred != rotated || !owned {
		t.Fatalf("live: %q %v", cred, owned)
	}
	if b, _ := credentials.ReadBackup(1, "a@b.co"); b == rotated {
		t.Fatal("adopted under a live session")
	}
	os.RemoveAll(filepath.Join(plan.Dir, "sessions"))
	if _, owned := sessionCredForUsage(1, seq.Get(1)); owned {
		t.Fatal("quiescent profile reported as owned")
	}
	if b, _ := credentials.ReadBackup(1, "a@b.co"); b != rotated {
		t.Fatalf("quiescent profile not adopted: %q", b)
	}
}

func TestRemoveAndMoveCarrySessionProfiles(t *testing.T) {
	sessionAccounts(t)
	probeAs(t, "a@b.co")
	plan, _ := PrepareSession("1", SessionOptions{})
	os.WriteFile(filepath.Join(plan.Dir, "history.jsonl"), []byte("h\n"), 0o600)

	markLive(t, plan.Dir)
	if _, _, err := Move("1", 5); err == nil {
		t.Fatal("moved under a live session")
	}
	if _, err := RemoveAccount("1"); err == nil {
		t.Fatal("removed under a live session")
	}
	os.RemoveAll(filepath.Join(plan.Dir, "sessions"))

	if _, _, err := Move("1", 5); err != nil {
		t.Fatal(err)
	}
	moved := session.Dir(5, "a@b.co")
	if raw, _ := os.ReadFile(filepath.Join(moved, "history.jsonl")); string(raw) != "h\n" {
		t.Fatal("profile history did not follow the move")
	}
	if _, err := os.Stat(plan.Dir); err == nil {
		t.Fatal("old profile left behind")
	}
	if _, err := RemoveAccount("5"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(moved); err == nil {
		t.Fatal("profile survived account removal")
	}
}

// TestSessionShellGuard: inside a `cswap run` shell (CLAUDE_CONFIG_DIR in
// <backup>/sessions/, however spelled) every roster or live-store change
// is refused; a CLAUDE_CONFIG_DIR elsewhere is not.
func TestSessionShellGuard(t *testing.T) {
	sessionAccounts(t)
	dir := session.Dir(1, "a@b.co")
	os.MkdirAll(dir, 0o700)
	resolved, _ := filepath.EvalSymlinks(dir)

	ops := map[string]func() error{
		"add":       func() error { _, _, err := Add(0, ""); return err },
		"switch":    func() error { _, err := SwitchTo("1", false); return err },
		"rotate":    func() error { _, err := Rotate(); return err },
		"remove":    func() error { _, err := RemoveAccount("1"); return err },
		"alias":     func() error { _, _, err := SetAlias("1", "work"); return err },
		"move":      func() error { _, _, err := Move("1", 5); return err },
		"add-token": func() error { _, err := AddToken("sk-ant-oat01-x", "", 0); return err },
	}
	for _, cfg := range []string{dir, resolved + string(filepath.Separator), filepath.Join(paths.SessionsDir(), "9-gone", "deeper")} {
		t.Setenv("CLAUDE_CONFIG_DIR", cfg)
		for name, op := range ops {
			if err := op(); !errors.Is(err, ErrSessionShell) {
				t.Errorf("CLAUDE_CONFIG_DIR=%s: %s was not refused: %v", cfg, name, err)
			}
		}
	}

	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if _, _, err := SetAlias("1", "work"); err != nil {
		t.Fatalf("an unrelated CLAUDE_CONFIG_DIR was refused: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", paths.SessionsDir()+"-other")
	if err := refuseSessionShell(); err != nil {
		t.Fatal("a sibling of the sessions dir was refused")
	}
}

// TestTokenStatusLines: an inactive account shows its session profile's
// token beside the stored backup (ignored when the profile was re-pointed),
// and the active account shows the live login's.
func TestTokenStatusLines(t *testing.T) {
	sessionAccounts(t)
	seq, _ := account.Load()
	now := time.Now()
	inactive := Snapshot{Slot: 1, Account: seq.Get(1)}

	lines := TokenStatusLines(inactive, now)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "stored backup: fresh, refresh token yes, expires ") {
		t.Fatalf("no profile: %q", lines)
	}

	dir := session.Dir(1, "a@b.co")
	cfg := []byte(`{"oauthAccount":{"emailAddress":"a@b.co"}}`)
	if err := session.Bootstrap(dir, credExp("at-a2", "", now.Add(-time.Hour).UnixMilli()), cfg); err != nil {
		t.Fatal(err)
	}
	lines = TokenStatusLines(inactive, now)
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "session profile: expired, refresh token no, expires ") ||
		!strings.HasPrefix(lines[1], "stored backup: ") {
		t.Fatalf("with profile: %q", lines)
	}

	os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"other@x.co"}}`), 0o600)
	if lines = TokenStatusLines(inactive, now); len(lines) != 2 || lines[0] != "session profile: ignored (different account)" {
		t.Fatalf("drifted profile: %q", lines)
	}

	active := Snapshot{Slot: 2, Account: seq.Get(2), Active: true}
	if lines = TokenStatusLines(active, now); len(lines) != 1 || !strings.HasPrefix(lines[0], "active profile: fresh, refresh token yes") {
		t.Fatalf("active: %q", lines)
	}
}
