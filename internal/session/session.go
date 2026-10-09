// Package session manages the per-account Claude Code profiles that
// `cswap run` launches: <backup root>/sessions/<slot>-<email slug>/, used as
// CLAUDE_CONFIG_DIR so one terminal runs as a stored account while the
// default ~/.claude login (and every other terminal) is left alone.
//
// The layout and every marker match claude-swap's session mode, so a profile
// made by either tool is reused by the other:
//
//   - the profile is seeded with a plaintext .credentials.json (Claude Code
//     migrates it into its own Keychain item, whose service name is
//     "Claude Code-credentials-" + sha256(NFC(CLAUDE_CONFIG_DIR))[:8]);
//   - .claude.json carries the account's oauthAccount plus the onboarding
//     keys Claude Code needs to skip its first-run flow;
//   - .cswap-shared.json records which shared items cswap linked in;
//   - .<profile>.cswap-stale-credentials, a SIBLING of the profile, flags a
//     profile whose backup changed while it was live.
//
// Once a session has run, the profile — not cswap's backup — holds the
// newest generation of the account's token family: Claude Code rotates the
// refresh token in place. Hence the two directions of coherence:
// AfterBackupWrite drops a profile's credential when the backup moves, and
// the switcher adopts a quiescent profile's newer credential into the backup
// before it would otherwise use (and so spend) the backup's consumed one.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/blairham/go-claude-swap/internal/keychain"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// File names inside (or beside) a profile, shared with claude-swap.
const (
	ShareManifest = ".cswap-shared.json"
	staleSuffix   = ".cswap-stale-credentials"
	credsFile     = ".credentials.json"
	configFile    = ".claude.json"
)

// SharedItems are mirrored from ~/.claude into a profile unless --no-share:
// user customizations only, never anything account- or instance-scoped.
var SharedItems = []string{"settings.json", "keybindings.json", "CLAUDE.md", "skills", "commands", "agents"}

// historyItems are linked additionally under --share-history: conversation
// transcripts (what `claude --resume` lists) and prompt history, so every
// account sees one history. POSIX only.
var historyItems = []string{"projects", "history.jsonl"}

// AuthOverrideEnv are variables that make Claude Code bypass account OAuth.
// A session launch scrubs them: `cswap run N` asks for account N, and an
// exported key would silently run as someone else.
var AuthOverrideEnv = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
	"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR",
}

// Slugify makes an email filesystem-safe (Windows-forbidden characters
// included). Uniqueness comes from the slot prefix, so it need not be
// injective.
func Slugify(email string) string {
	var b strings.Builder
	for _, r := range norm.NFC.String(email) {
		if r < 0x80 && (r == '.' || r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Dir is the profile directory for a slot's account.
func Dir(slot int, email string) string {
	return filepath.Join(paths.SessionsDir(), strconv.Itoa(slot)+"-"+Slugify(email))
}

// KeychainService is the Keychain service Claude Code derives for a config
// dir: it hashes the exported CLAUDE_CONFIG_DIR verbatim (NFC, unresolved),
// so this must be given exactly the string that is exported.
func KeychainService(configDir string) string {
	sum := sha256.Sum256([]byte(norm.NFC.String(configDir)))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// keychainServices lists the services that may hold a profile's credential:
// its own name, then a symlinked profile's target's.
func keychainServices(dir string) []string {
	out := []string{KeychainService(dir)}
	if target, err := os.Readlink(dir); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(dir), target)
		}
		out = append(out, KeychainService(target))
	}
	return out
}

// DeleteKeychainEntry removes a profile's hashed Keychain item (best-effort;
// a no-op off macOS). Needed before seeding — Claude Code reads the Keychain
// before the file, so a stale item would shadow a fresh seed — and before
// the directory goes, since the name cannot be derived afterwards.
func DeleteKeychainEntry(dir string) {
	if keychain.Available() {
		_ = keychain.Delete(KeychainService(dir), keychain.UserAccountName())
	}
}

// --- stale marker ---------------------------------------------------------

// staleMarker is where a profile's stale flag is written: beside the
// profile, not inside it, so a fault on the profile directory itself cannot
// also stop the flag from landing.
func staleMarker(dir string) string {
	return filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+staleSuffix)
}

// IsStale reports whether a profile is flagged for re-bootstrap (either the
// sibling marker or the legacy in-profile one).
func IsStale(dir string) bool {
	for _, p := range []string{staleMarker(dir), filepath.Join(dir, staleSuffix)} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// MarkStale flags a live profile for re-bootstrap once nothing runs in it.
func MarkStale(dir string) error {
	f, err := os.OpenFile(staleMarker(dir), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// ClearStale removes both marker locations; reports whether both are gone.
func ClearStale(dir string) bool {
	ok := true
	for _, p := range []string{staleMarker(dir), filepath.Join(dir, staleSuffix)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			ok = false
		}
	}
	return ok
}

// --- liveness -------------------------------------------------------------

// Live is what a scan of a profile's sessions/<pid>.json records found.
type Live struct {
	PIDs []int
	// Unreadable counts records that could not be parsed. A guard must treat
	// these as live: not knowing is not the same as knowing nothing runs.
	Unreadable int
}

// Busy reports whether anything is, or may be, running in the profile.
func (l Live) Busy() bool { return len(l.PIDs) > 0 || l.Unreadable > 0 }

// ScanLive reads the PID records Claude Code writes into a profile and keeps
// those whose process is alive. A recycled PID reads as live, which only
// ever makes a guard more cautious.
func ScanLive(dir string) Live {
	var l Live
	entries, err := os.ReadDir(filepath.Join(dir, "sessions"))
	if err != nil {
		return l
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "sessions", e.Name()))
		var rec struct {
			PID *int64 `json:"pid"`
		}
		if err != nil || json.Unmarshal(raw, &rec) != nil || rec.PID == nil {
			l.Unreadable++
			continue
		}
		if pid := int(*rec.PID); processAlive(pid) {
			l.PIDs = append(l.PIDs, pid)
		}
	}
	return l
}

// Quiescent reports that nothing runs in the profile and every record was
// readable — the precondition for rewriting or removing it.
func Quiescent(dir string) bool { return !ScanLive(dir).Busy() }

// --- credentials and identity ---------------------------------------------

// ReadCredentials is a best-effort read of the profile's current credential:
// its hashed Keychain item on macOS (which shadows the seed once Claude Code
// has written it), else the plaintext seed. "" when there is none.
func ReadCredentials(dir string) string {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	if keychain.Available() {
		for _, svc := range keychainServices(dir) {
			v, err := keychain.Get(svc, keychain.UserAccountName())
			if err == nil && v != "" {
				return v
			}
			if err != nil && !errors.Is(err, keychain.ErrNotFound) {
				break // unreadable: the seed is the next-best truth
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, credsFile))
	if err != nil {
		return ""
	}
	return string(raw)
}

// mayHoldCredential is false only when every store is positively empty. An
// unreadable Keychain leans present: the profile may hold the newest
// generation of the account's token family.
func mayHoldCredential(dir string) bool {
	if raw, err := os.ReadFile(filepath.Join(dir, credsFile)); err == nil && len(raw) > 0 {
		return true
	}
	if !keychain.Available() {
		return false
	}
	for _, svc := range keychainServices(dir) {
		v, err := keychain.Get(svc, keychain.UserAccountName())
		if err == nil && v != "" {
			return true
		}
		if err != nil && !errors.Is(err, keychain.ErrNotFound) {
			return true
		}
	}
	return false
}

// Identity reads the account a profile is logged in as, from its
// .claude.json oauthAccount. ok is false when none is readable.
func Identity(dir string) (email, org string, ok bool) {
	raw, err := os.ReadFile(filepath.Join(dir, configFile))
	if err != nil {
		return "", "", false
	}
	var cfg struct {
		OAuthAccount *struct {
			Email string  `json:"emailAddress"`
			Org   *string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.OAuthAccount == nil || cfg.OAuthAccount.Email == "" {
		return "", "", false
	}
	if cfg.OAuthAccount.Org != nil {
		org = *cfg.OAuthAccount.Org
	}
	return cfg.OAuthAccount.Email, org, true
}

// Drifted reports whether an in-session /login re-pointed the profile at a
// different account than its slot. The org is compared only when both sides
// have one; an unreadable identity is not drift.
func Drifted(dir, email, org string) bool {
	pe, po, ok := Identity(dir)
	if !ok {
		return false
	}
	return pe != email || (po != "" && org != "" && po != org)
}

// --- lifecycle --------------------------------------------------------------

// Invalidate drops a profile's credential material, keeping its history, so
// the next `cswap run` re-bootstraps from the backup.
func Invalidate(dir string) error {
	DeleteKeychainEntry(dir)
	if err := os.Remove(filepath.Join(dir, credsFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ClearStale(dir)
	return nil
}

// AfterBackupWrite keeps a slot's profile coherent with its backup after the
// backup changed (re-login, add, import, a refresh, a switch backing up): a
// profile seeded from the old credential may hold a rotated-out token that
// still passes the local reuse check. A quiescent profile is invalidated at
// once; a live one is left to its running Claude Code and flagged stale.
func AfterBackupWrite(slot int, email string) {
	dir := Dir(slot, email)
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if ScanLive(dir).Busy() || Invalidate(dir) != nil {
		_ = MarkStale(dir)
	}
}

// Remove deletes a profile, its Keychain item, and its stale marker.
func Remove(dir string) error {
	if _, err := os.Stat(dir); err == nil {
		DeleteKeychainEntry(dir)
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if !ClearStale(dir) {
		return fmt.Errorf("could not clear the stale marker for %s", dir)
	}
	return nil
}

// Bootstrap seeds a profile from an account's backup: the credential as a
// plaintext .credentials.json, and the backup config's oauthAccount merged
// into the profile's .claude.json (so a re-bootstrap keeps the profile's own
// projects and history). The caller holds cswap's lock.
func Bootstrap(dir, cred string, configBackup []byte) error {
	var cfg map[string]json.RawMessage
	if json.Unmarshal(configBackup, &cfg) != nil || len(cfg["oauthAccount"]) == 0 || string(cfg["oauthAccount"]) == "null" {
		return errors.New("no stored config backup with an oauthAccount")
	}
	// Claude Code reads the Keychain before the file; a stale item from an
	// earlier profile at this path would shadow the seed.
	DeleteKeychainEntry(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(dir, 0o700)
	}
	if err := writePrivate(filepath.Join(dir, credsFile), []byte(cred)); err != nil {
		return err
	}

	existing := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(filepath.Join(dir, configFile)); err == nil {
		if json.Unmarshal(raw, &existing) != nil || existing == nil {
			existing = map[string]json.RawMessage{}
		}
	}
	existing["oauthAccount"] = cfg["oauthAccount"]
	existing["hasCompletedOnboarding"] = json.RawMessage("true")
	// Claude Code shows onboarding unless theme is set as well.
	if _, ok := existing["theme"]; !ok {
		theme := cfg["theme"]
		if len(theme) == 0 || string(theme) == "null" || string(theme) == `""` {
			theme = json.RawMessage(`"dark"`)
		}
		existing["theme"] = theme
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, configFile), data)
}

func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// --- validation -------------------------------------------------------------

// Verdict is the outcome of probing a profile with `claude auth status`.
type Verdict int

// Verdicts. Unknown (the probe ran but did not answer) and Unreachable
// (claude could not be run) are failures of the probe, not of the profile,
// and must never be acted on as Invalid — Invalid deletes the profile.
const (
	Valid Verdict = iota
	Invalid
	Unknown
	Unreachable
)

const probeTimeout = 10 * time.Second

// Status is what `claude auth status --json` reports about a profile.
type Status struct {
	LoggedIn   *bool  `json:"loggedIn"`
	AuthMethod string `json:"authMethod"`
	Email      string `json:"email"`
	OrgID      string `json:"orgId"`
}

// Probe runs `claude auth status --json` against a profile (a local check;
// it makes no API call). It returns Valid with the parsed status when the
// command answered, else the probe failure. A variable so tests never launch
// the real claude.
var Probe = func(dir string) (Verdict, Status) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return Unreachable, Status{}
	}
	cmd := exec.Command(claude, "auth", "status", "--json")
	cmd.Env = append(ScrubbedEnv(os.Environ()), "CLAUDE_CONFIG_DIR="+dir)
	out, err := runWithTimeout(cmd, probeTimeout)
	if errors.Is(err, errTimeout) {
		return Unknown, Status{}
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return Invalid, Status{}
		}
		return Unreachable, Status{}
	}
	var st Status
	if json.Unmarshal(out, &st) != nil {
		return Unknown, Status{}
	}
	return Valid, st
}

var errTimeout = errors.New("timed out")

func runWithTimeout(cmd *exec.Cmd, d time.Duration) ([]byte, error) {
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return []byte(out.String()), err
	case <-time.After(d):
		_ = cmd.Process.Kill()
		<-done
		return nil, errTimeout
	}
}

// Check validates a profile for an account: logged in through claude.ai as
// that email (and org, when both sides name one).
func Check(dir, email, org string) Verdict {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return Invalid
	}
	v, st := Probe(dir)
	if v != Valid {
		return v
	}
	if st.LoggedIn == nil || !*st.LoggedIn || st.AuthMethod != "claude.ai" || st.Email != email {
		return Invalid
	}
	if st.OrgID != "" && org != "" && st.OrgID != org {
		return Invalid
	}
	return Valid
}

// Usable is the reuse judgement: Valid, or Unknown with local artifacts that
// say the profile still holds this account's credential. Unreachable is
// never usable — no file says whether claude can be run.
func Usable(v Verdict, dir, email, org string) bool {
	switch v {
	case Valid:
		return true
	case Unknown:
		return mayHoldCredential(dir) && !Drifted(dir, email, org)
	default:
		return false
	}
}

// ScrubbedEnv drops the auth-override variables from an environment.
func ScrubbedEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		drop := name == "CLAUDE_CONFIG_DIR"
		for _, v := range AuthOverrideEnv {
			if name == v {
				drop = true
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}
