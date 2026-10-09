package switcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/session"
)

// bootstrapLockTimeout gives the session bootstrap, which holds cswap's
// lock across `claude auth status` probes, more headroom than a switch.
const bootstrapLockTimeout = 30 * time.Second

// SessionPlan is what `cswap run` should launch.
type SessionPlan struct {
	Slot  int
	Email string
	// Direct means the account is already the default login: launch plain
	// claude rather than a second, drifting copy of the same credential.
	Direct bool
	// Dir is the profile to use as CLAUDE_CONFIG_DIR (unset when Direct).
	Dir string
	// Notes are warnings and remarks for the user.
	Notes []string
}

// SessionOptions tune PrepareSession.
type SessionOptions struct {
	// Share mirrors ~/.claude customizations (and user-scope MCP servers)
	// into the profile.
	Share bool
	// ShareHistory links ~/.claude's conversation history into the profile,
	// merging the profile's own history into ~/.claude first.
	ShareHistory bool
	// RequireSession refuses the Direct fast path instead of taking it.
	RequireSession bool
}

// PrepareSession resolves an account and makes sure a usable session
// profile exists for it: reused when `claude auth status` vouches for it,
// otherwise (re)seeded from the account's backup.
func PrepareSession(selector string, opts SessionOptions) (*SessionPlan, error) {
	seq, err := account.Load()
	if err != nil {
		return nil, err
	}
	slot, err := seq.Resolve(selector)
	if err != nil {
		return nil, err
	}
	a := seq.Get(slot)
	plan := &SessionPlan{Slot: slot, Email: a.Email}

	if a.Kind == account.KindAPIKey {
		return nil, fmt.Errorf("account %d (%s) is an API-key account; 'cswap run' (session mode) does not support API-key accounts — use 'cswap switch %d' to make it the default login instead", slot, a.Email, slot)
	}

	if preset := os.Getenv("CLAUDE_CONFIG_DIR"); preset != "" {
		// "The default login" means nothing here (this may be a session
		// terminal already), so there is no fast path.
		plan.Notes = append(plan.Notes, fmt.Sprintf("CLAUDE_CONFIG_DIR is already set (%s); overriding it for this launch.", preset))
	} else if id, _ := claudecfg.ReadIdentity(); id != nil && id.Email == a.Email && id.OrganizationUUID == a.OrganizationUUID {
		if opts.RequireSession {
			return nil, fmt.Errorf("account %d (%s) is the active default login, so this launch would run plain claude on the default login rather than in a session profile; switch the default login to another account first, or run claude directly", slot, a.Email)
		}
		plan.Direct = true
		plan.Notes = append(plan.Notes, fmt.Sprintf("Account-%d (%s) is already the active default login — launching claude directly.", slot, a.Email))
		return plan, nil
	}

	dir := session.Dir(slot, a.Email)
	plan.Dir = dir
	share := func() { plan.Notes = append(plan.Notes, session.SyncSharing(dir, opts.Share, opts.ShareHistory)...) }

	// Deferred invalidation: the backup moved while this profile was live.
	// Honored only once nothing runs in it.
	stale := session.IsStale(dir) && session.Quiescent(dir)
	if !stale && session.Usable(session.Check(dir, a.Email, a.OrganizationUUID), dir, a.Email, a.OrganizationUUID) {
		share()
		return plan, nil
	}

	// About to seed from the backup: make it the newest generation first
	// (a profile that rotated past it is adopted), then freshen it — all
	// before the lock, since a refresh is network I/O. Never while Claude
	// Code runs in the profile: refreshing would spend the very refresh
	// token that running instance holds.
	if session.Quiescent(dir) {
		if _, err := adoptSessionCredential(slot, a.Email, a.OrganizationUUID); err != nil {
			plan.Notes = append(plan.Notes, fmt.Sprintf("could not adopt the session profile's credential: %v", err))
		}
		_, warns, ferr := freshenTarget(slot, a.Email, false)
		if ferr != nil {
			return nil, ferr
		}
		plan.Notes = append(plan.Notes, warns...)
	}

	lock := locks.NewFileLock(paths.LockPath())
	lock.Timeout = bootstrapLockTimeout
	if err := lock.Acquire(); err != nil {
		return nil, err
	}
	defer lock.Release()

	// Another `cswap run` may have bootstrapped while we waited.
	if session.IsStale(dir) && session.Quiescent(dir) {
		if err := session.Invalidate(dir); err != nil {
			return nil, fmt.Errorf("could not invalidate the stale session profile: %w", err)
		}
	}
	if session.Usable(session.Check(dir, a.Email, a.OrganizationUUID), dir, a.Email, a.OrganizationUUID) {
		share()
		return plan, nil
	}

	cred, unreadable := credentials.ReadBackup(slot, a.Email)
	if unreadable {
		return nil, fmt.Errorf("account %d's backup is unreadable right now (Keychain locked or no GUI session) — retry from a GUI terminal; do not re-add", slot)
	}
	if cred == "" {
		return nil, fmt.Errorf("account %d has no stored credentials — run 'cswap login %d' to authenticate it", slot, slot)
	}
	if credentials.IsAPIKey(cred) {
		return nil, fmt.Errorf("account %d (%s) holds an API key; 'cswap run' supports OAuth accounts only", slot, a.Email)
	}
	cfg, err := os.ReadFile(paths.AccountConfigBackup(slot, a.Email))
	if err != nil {
		return nil, fmt.Errorf("account %d has no stored config backup — run 'cswap login %d': %w", slot, slot, err)
	}
	if !session.Quiescent(dir) {
		return nil, fmt.Errorf("account %d's session profile is in use by a running Claude Code but does not validate; exit that session and retry", slot)
	}
	if err := session.Bootstrap(dir, cred, cfg); err != nil {
		return nil, fmt.Errorf("seeding the session profile for Account-%d: %w", slot, err)
	}
	share()

	switch v := session.Check(dir, a.Email, a.OrganizationUUID); {
	case v == session.Valid, v == session.Unknown && session.Usable(v, dir, a.Email, a.OrganizationUUID):
		return plan, nil
	case v == session.Unknown, v == session.Unreachable:
		// A probe that did not answer says nothing about the profile: keep it.
		return nil, fmt.Errorf("the session profile for Account-%d (%s) could not be verified: 'claude auth status' did not run or did not answer; the profile is left in place — check that claude is on PATH, then retry", slot, a.Email)
	default:
		_ = session.Remove(dir)
		return nil, fmt.Errorf("the session profile for Account-%d (%s) failed validation — run 'cswap login %d' to re-authenticate it", slot, a.Email, slot)
	}
}

// profileAhead returns a slot's session-profile credential when it is a
// newer generation of the slot's token family than the stored backup, else
// "". Claude Code rotates the family inside a profile and the backup never
// follows, so after a session that refreshed, the backup holds a consumed
// generation whose first refresh would get invalid_grant. Generations are
// ordered by access-token expiry: every refresh issues a later one. A
// re-pointed (drifted) or stale profile, and anything unreadable, is never
// ahead.
func profileAhead(slot int, email, org string) string {
	dir := session.Dir(slot, email)
	if session.IsStale(dir) || session.Drifted(dir, email, org) {
		return ""
	}
	profile := session.ReadCredentials(dir)
	if strings.TrimSpace(profile) == "" || credentials.IsAPIKey(profile) {
		return ""
	}
	backup, unreadable := credentials.ReadBackup(slot, email)
	if unreadable {
		return ""
	}
	if oauth.Fingerprint([]byte(profile)) == oauth.Fingerprint([]byte(backup)) {
		return ""
	}
	pb, perr := oauth.ParseBlob([]byte(profile))
	if perr != nil || pb.OAuth == nil {
		return ""
	}
	var backupExp int64
	if bb, err := oauth.ParseBlob([]byte(backup)); err == nil && bb.OAuth != nil {
		backupExp = bb.OAuth.ExpiresAt
	}
	if pb.OAuth.ExpiresAt > backupExp {
		return profile
	}
	return ""
}

// adoptSessionCredential advances a slot's backup to its quiescent session
// profile's newer credential (the complement of session.AfterBackupWrite).
// Decided and written under cswap's lock; the write skips the session
// invalidation, since the two copies now hold the same generation.
func adoptSessionCredential(slot int, email, org string) (bool, error) {
	dir := session.Dir(slot, email)
	if _, err := os.Stat(dir); err != nil {
		return false, nil
	}
	lock := locks.NewFileLock(paths.LockPath())
	if err := lock.Acquire(); err != nil {
		return false, err
	}
	defer lock.Release()
	return adoptLocked(slot, email, org)
}

// adoptLocked is adoptSessionCredential for a caller already holding
// cswap's lock (which is not re-entrant).
func adoptLocked(slot int, email, org string) (bool, error) {
	dir := session.Dir(slot, email)
	if !session.Quiescent(dir) {
		return false, nil
	}
	profile := profileAhead(slot, email, org)
	if profile == "" {
		return false, nil
	}
	if err := credentials.WriteBackupKeepSession(slot, email, profile); err != nil {
		return false, err
	}
	return true, nil
}

// reconcileSessionBeforeActivation runs before a slot is made the default
// login. A live session whose profile rotated past the backup makes the
// backup a consumed generation, so activation is refused; otherwise a
// quiescent profile's newer credential is adopted first.
func reconcileSessionBeforeActivation(slot int, a *account.Account) ([]string, error) {
	dir := session.Dir(slot, a.Email)
	if _, err := os.Stat(dir); err != nil {
		return nil, nil
	}
	live := session.ScanLive(dir)
	if live.Busy() {
		if profileAhead(slot, a.Email, a.OrganizationUUID) != "" {
			return nil, fmt.Errorf("account %d (%s) has a live session-mode Claude Code, and its session profile's credential has rotated past the stored backup: activating the backup would fail on its first refresh — exit the session (its credential is adopted once nothing runs in it), or switch to another account", slot, a.Email)
		}
		if len(live.PIDs) > 0 {
			return []string{fmt.Sprintf("Account-%d (%s) also has a live session-mode Claude Code; if that session later fails to authenticate, exit it and re-run 'cswap run %d'", slot, a.Email, slot)}, nil
		}
		return nil, nil
	}
	if _, err := adoptSessionCredential(slot, a.Email, a.OrganizationUUID); err != nil {
		return []string{fmt.Sprintf("could not adopt Account-%d's session profile credential: %v", slot, err)}, nil
	}
	return nil, nil
}

// sessionCredForUsage resolves which credential a usage fetch for an
// inactive slot should use. A quiescent profile that rotated past the
// backup is adopted (the backup becomes the head). A live one is used
// read-only: its running Claude Code owns the family, so the caller must
// not refresh it (owned=true).
func sessionCredForUsage(slot int, a *account.Account) (cred string, owned bool) {
	dir := session.Dir(slot, a.Email)
	if _, err := os.Stat(dir); err != nil {
		return "", false
	}
	if session.ScanLive(dir).Busy() {
		return profileAhead(slot, a.Email, a.OrganizationUUID), true
	}
	_, _ = adoptSessionCredential(slot, a.Email, a.OrganizationUUID)
	return "", false
}

// ensureNoLiveSession refuses a destructive roster operation while Claude
// Code runs in (or may run in) the account's session profile.
func ensureNoLiveSession(slot int, email, action string) error {
	dir := session.Dir(slot, email)
	live := session.ScanLive(dir)
	switch {
	case len(live.PIDs) > 0:
		return fmt.Errorf("account %d (%s) has a live session-mode Claude Code (PID %v); exit it first, then retry %s", slot, email, live.PIDs, action)
	case live.Unreadable > 0:
		return fmt.Errorf("account %d (%s) has %d session record(s) that could not be read, so whether Claude Code is running cannot be told; inspect %s, then retry %s",
			slot, email, live.Unreadable, filepath.Join(dir, "sessions"), action)
	}
	return nil
}

// relocateProfiles renames session profiles to follow a slot move, keeping
// their history. The profile's hashed Keychain item is named after the old
// path and cannot follow, so the newest credential is adopted into the
// backup first and the moved profile re-seeds from it on its next run.
// The caller holds cswap's lock.
func relocateProfiles(moves map[int]int, accts map[int]*account.Account) error {
	type step struct{ from, tmp, to string }
	var steps []step
	for from, to := range moves {
		a := accts[from]
		src := session.Dir(from, a.Email)
		if _, err := os.Stat(src); err != nil {
			session.ClearStale(src)
			continue
		}
		if _, err := adoptLocked(from, a.Email, a.OrganizationUUID); err != nil {
			return err
		}
		session.DeleteKeychainEntry(src)
		session.ClearStale(src)
		steps = append(steps, step{src, src + ".moving", session.Dir(to, a.Email)})
	}
	// Two phases so a swap's two renames cannot collide.
	for _, s := range steps {
		if err := os.Rename(s.from, s.tmp); err != nil {
			return err
		}
	}
	var errs []error
	for _, s := range steps {
		if err := os.Rename(s.tmp, s.to); err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, session.Invalidate(s.to))
	}
	return errors.Join(errs...)
}
