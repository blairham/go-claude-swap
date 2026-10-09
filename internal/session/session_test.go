package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CSWAP_DISABLE_KEYCHAIN", "1")
	return home
}

// Reference values computed with claude-swap's session.py
// (slugify_email, keychain_service_name).
func TestNamesMatchOriginal(t *testing.T) {
	if got := Slugify("Me+tag@Exämple.com"); got != "Me_tag_Ex_mple.com" {
		t.Errorf("Slugify = %q", got)
	}
	if got := Slugify("a b/c@x.co"); got != "a_b_c_x.co" {
		t.Errorf("Slugify = %q", got)
	}
	if got := KeychainService("/Users/u/.claude-swap-backup/sessions/2-me_example.com"); got != "Claude Code-credentials-576dce4a" {
		t.Errorf("KeychainService = %q", got)
	}
	// NFD input hashes as its NFC form, as Claude Code does.
	if got := KeychainService("/home/café/s"); got != "Claude Code-credentials-b0038e64" {
		t.Errorf("KeychainService(NFD) = %q", got)
	}
	env(t)
	if got := filepath.Base(Dir(2, "me@example.com")); got != "2-me_example.com" {
		t.Errorf("Dir = %q", got)
	}
}

// TestStaleMarkerLocation pins the original's marker: a dot-prefixed
// sibling of the profile, plus the legacy in-profile location for reads.
func TestStaleMarkerLocation(t *testing.T) {
	env(t)
	dir := Dir(2, "me@example.com")
	os.MkdirAll(dir, 0o700)
	if IsStale(dir) {
		t.Fatal("fresh profile reads stale")
	}
	if err := MarkStale(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), ".2-me_example.com.cswap-stale-credentials")); err != nil {
		t.Fatalf("marker not at the original's path: %v", err)
	}
	ClearStale(dir)
	os.WriteFile(filepath.Join(dir, ".cswap-stale-credentials"), nil, 0o600)
	if !IsStale(dir) {
		t.Fatal("legacy in-profile marker ignored")
	}
	ClearStale(dir)
	if IsStale(dir) {
		t.Fatal("ClearStale left a marker")
	}
}

func writeRecord(t *testing.T, dir, name, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "sessions"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "sessions", name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanLive(t *testing.T) {
	env(t)
	dir := Dir(1, "a@b.co")
	if ScanLive(dir).Busy() {
		t.Fatal("missing profile reads busy")
	}
	writeRecord(t, dir, "dead.json", `{"pid": 2147483646}`)
	if ScanLive(dir).Busy() {
		t.Fatal("dead pid reads live")
	}
	writeRecord(t, dir, "me.json", fmt.Sprintf(`{"pid": %d, "sessionId": "x"}`, os.Getpid()))
	if l := ScanLive(dir); !slices.Equal(l.PIDs, []int{os.Getpid()}) {
		t.Fatalf("live = %+v", l)
	}
	os.Remove(filepath.Join(dir, "sessions", "me.json"))
	writeRecord(t, dir, "bad.json", `[1,2]`)
	if l := ScanLive(dir); l.Unreadable != 1 || !l.Busy() || Quiescent(dir) {
		t.Fatalf("an unreadable record must count as busy: %+v", l)
	}
}

func TestBootstrapMergesConfig(t *testing.T) {
	env(t)
	dir := Dir(1, "a@b.co")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"projects":{"/p":{}},"theme":"light"}`), 0o600)
	cfg := []byte(`{"oauthAccount":{"emailAddress":"a@b.co","organizationUuid":null},"theme":"dark","projects":{"/machine":{}}}`)
	if err := Bootstrap(dir, `{"claudeAiOauth":{"accessToken":"at"}}`, cfg); err != nil {
		t.Fatal(err)
	}
	if got := ReadCredentials(dir); got != `{"claudeAiOauth":{"accessToken":"at"}}` {
		t.Fatalf("seed = %q", got)
	}
	if fi, _ := os.Stat(filepath.Join(dir, ".credentials.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("seed mode = %v", fi.Mode())
	}
	var got map[string]any
	raw, _ := os.ReadFile(filepath.Join(dir, ".claude.json"))
	json.Unmarshal(raw, &got)
	if got["hasCompletedOnboarding"] != true || got["theme"] != "light" {
		t.Fatalf("config = %s", raw)
	}
	if _, ok := got["projects"].(map[string]any)["/p"]; !ok {
		t.Fatalf("profile's own projects lost: %s", raw)
	}
	if email, _, ok := Identity(dir); !ok || email != "a@b.co" {
		t.Fatalf("identity = %q %v", email, ok)
	}
	if err := Bootstrap(dir, "x", []byte(`{"projects":{}}`)); err == nil {
		t.Fatal("bootstrap without an oauthAccount succeeded")
	}
}

func TestAfterBackupWrite(t *testing.T) {
	env(t)
	dir := Dir(1, "a@b.co")
	cfg := []byte(`{"oauthAccount":{"emailAddress":"a@b.co"}}`)

	// Quiescent: the credential goes at once, history stays.
	Bootstrap(dir, "cred", cfg)
	AfterBackupWrite(1, "a@b.co")
	if ReadCredentials(dir) != "" || IsStale(dir) {
		t.Fatal("quiescent profile not invalidated")
	}
	if _, _, ok := Identity(dir); !ok {
		t.Fatal("invalidation took the profile config too")
	}

	// Live: left alone for its running Claude Code, flagged stale.
	Bootstrap(dir, "cred", cfg)
	writeRecord(t, dir, "me.json", fmt.Sprintf(`{"pid": %d}`, os.Getpid()))
	AfterBackupWrite(1, "a@b.co")
	if ReadCredentials(dir) != "cred" || !IsStale(dir) {
		t.Fatal("live profile was rewritten, or not flagged stale")
	}

	// No profile: nothing created.
	AfterBackupWrite(9, "z@b.co")
	if _, err := os.Stat(Dir(9, "z@b.co")); err == nil {
		t.Fatal("AfterBackupWrite created a profile")
	}
}

func TestCheck(t *testing.T) {
	env(t)
	dir := Dir(1, "a@b.co")
	yes := true
	stub := func(v Verdict, st Status) {
		t.Helper()
		prev := Probe
		Probe = func(string) (Verdict, Status) { return v, st }
		t.Cleanup(func() { Probe = prev })
	}
	stub(Valid, Status{LoggedIn: &yes, AuthMethod: "claude.ai", Email: "a@b.co"})
	if Check(dir, "a@b.co", "") != Invalid {
		t.Fatal("missing dir must be Invalid")
	}
	os.MkdirAll(dir, 0o700)
	if Check(dir, "a@b.co", "org") != Valid {
		t.Fatal("matching status rejected")
	}
	if Check(dir, "other@b.co", "") != Invalid {
		t.Fatal("another account's login accepted")
	}
	stub(Valid, Status{LoggedIn: &yes, AuthMethod: "claude.ai", Email: "a@b.co", OrgID: "o1"})
	if Check(dir, "a@b.co", "o2") != Invalid {
		t.Fatal("org mismatch accepted")
	}
	stub(Valid, Status{LoggedIn: &yes, AuthMethod: "api_key", Email: "a@b.co"})
	if Check(dir, "a@b.co", "") != Invalid {
		t.Fatal("non-claude.ai login accepted")
	}
	stub(Unknown, Status{})
	if Usable(Check(dir, "a@b.co", ""), dir, "a@b.co", "") {
		t.Fatal("a timed-out probe over an empty profile reads usable")
	}
	os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte("cred"), 0o600)
	if !Usable(Check(dir, "a@b.co", ""), dir, "a@b.co", "") {
		t.Fatal("a timed-out probe over a seeded profile reads unusable")
	}
	if Usable(Unreachable, dir, "a@b.co", "") {
		t.Fatal("unreachable claude reads usable")
	}
}

func TestScrubbedEnv(t *testing.T) {
	got := ScrubbedEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=k", "CLAUDE_CONFIG_DIR=/x", "CLAUDE_CODE_OAUTH_TOKEN=t", "HOME=/h"})
	if strings.Join(got, ",") != "PATH=/bin,HOME=/h" {
		t.Fatalf("env = %v", got)
	}
}
