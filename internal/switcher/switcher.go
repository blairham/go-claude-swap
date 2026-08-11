// Package switcher implements the account lifecycle: capturing logins,
// switching the live Claude Code credential between slots, and roster
// maintenance.
//
// The switch sequence holds three locks in a fixed order — cswap's own file
// lock, Claude Code's credential locks, then the config lock — performs only
// local I/O while holding them, and rolls back completed steps in reverse on
// failure.
package switcher

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// Result reports a completed (or no-op) switch.
type Result struct {
	Switched   bool
	FromSlot   int
	FromEmail  string
	ToSlot     int
	ToEmail    string
	Reason     string // "switched", "already-active", "only-one-account", ...
	Warnings   []string
	UsedForce  bool
	NewBackend string // "keychain" or "file" — drives the restart hint
}

// Add captures the current Claude Code login as a managed account. slot 0
// means auto-assign; alias "" means none.
func Add(slot int, alias string) (int, string, error) {
	if err := paths.EnsureDirs(); err != nil {
		return 0, "", err
	}
	if alias != "" {
		if err := account.ValidateAlias(alias); err != nil {
			return 0, "", err
		}
	}

	id, err := claudecfg.ReadIdentity()
	if err != nil {
		return 0, "", err
	}
	if id == nil {
		return 0, "", errors.New("no active Claude account found — log in with Claude Code first")
	}

	active := credentials.ReadActive()
	if active.Unreadable {
		return 0, "", errors.New("failed to read the current credentials")
	}
	if active.Degraded {
		return 0, "", errors.New("the Keychain could not be read (a plaintext fallback answered, but it may be stale) — retry from a GUI session")
	}
	if active.Value == "" {
		return 0, "", errors.New("no credentials found for the current login")
	}
	if credentials.IsAPIKey(active.Value) {
		return 0, "", errors.New("the current login uses a managed API key; use 'cswap add-token' instead")
	}

	cfgText, err := claudecfg.ReadRaw()
	if err != nil {
		return 0, "", fmt.Errorf("cannot read %s: %w", paths.GlobalConfigPath(), err)
	}

	seq, err := account.Load()
	if err != nil {
		return 0, "", err
	}
	if alias != "" {
		if conflict := aliasConflict(seq, alias, id.Email, id.OrganizationUUID); conflict != "" {
			return 0, "", fmt.Errorf("alias %q is already used by %s", alias, conflict)
		}
	}

	// Dedup by identity: refresh the existing slot in place.
	target := seq.FindByIdentity(id.Email, id.OrganizationUUID)
	if target == 0 {
		if slot > 0 {
			if existing := seq.Get(slot); existing != nil &&
				(existing.Email != id.Email || existing.OrganizationUUID != id.OrganizationUUID) {
				return 0, "", fmt.Errorf("slot %d is occupied by %s (remove it first)", slot, existing.Email)
			}
			target = slot
		} else {
			target = seq.NextSlot()
		}
	}

	if err := credentials.WriteBackup(target, id.Email, active.Value); err != nil {
		return 0, "", fmt.Errorf("writing credential backup: %w", err)
	}
	if err := account.WriteFileAtomic(paths.AccountConfigBackup(target, id.Email), cfgText, 0o600); err != nil {
		return 0, "", fmt.Errorf("writing config backup: %w", err)
	}
	// A fresh credential invalidates any dead-token strike (best-effort:
	// the cache heals itself on the next fetch).
	_ = usage.Update(func(s *usage.Store) {
		if e := s.Get(target, id.Email, id.OrganizationUUID); e != nil {
			e.ClearDeadToken()
		}
	})

	rec := seq.Get(target)
	if rec == nil {
		rec = &account.Account{Added: time.Now().UTC().Format(account.TimeFormat)}
	}
	rec.Email = id.Email
	rec.UUID = id.AccountUUID
	rec.OrganizationUUID = id.OrganizationUUID
	rec.OrganizationName = id.OrganizationName
	if alias != "" {
		rec.Alias = alias
	}
	seq.Upsert(target, rec)
	seq.ActiveAccountNumber = &target
	if err := seq.Save(); err != nil {
		return 0, "", err
	}
	return target, id.Email, nil
}

func aliasConflict(seq *account.Sequence, alias, email, orgUUID string) string {
	for _, a := range seq.Accounts {
		if a.Alias == alias && (a.Email != email || a.OrganizationUUID != orgUUID) {
			return a.Email
		}
	}
	return ""
}

// SwitchTo activates the account matching selector. force skips the
// already-active guard and the backup of the outgoing account.
func SwitchTo(selector string, force bool) (*Result, error) {
	seq, err := account.Load()
	if err != nil {
		return nil, err
	}
	target, err := seq.Resolve(selector)
	if err != nil {
		return nil, err
	}
	return performSwitch(seq, target, force)
}

// Rotate advances to the next switchable account in sequence order.
func Rotate() (*Result, error) {
	seq, err := account.Load()
	if err != nil {
		return nil, err
	}
	if len(seq.Accounts) < 2 {
		return &Result{Reason: "only-one-account"}, nil
	}
	curSlot, _ := seq.Active()

	order := seq.Order
	start := indexOf(order, curSlot)
	var warnings []string
	for i := 1; i <= len(order); i++ {
		slot := order[(start+i)%len(order)]
		if slot == curSlot {
			break
		}
		a := seq.Get(slot)
		if a == nil {
			continue
		}
		if a.Disabled {
			warnings = append(warnings, fmt.Sprintf("Skipped Account-%d (disabled)", slot))
			continue
		}
		if !Switchable(slot, a) {
			warnings = append(warnings, fmt.Sprintf("Skipped Account-%d (no stored credentials/config)", slot))
			continue
		}
		res, serr := performSwitch(seq, slot, false)
		if serr != nil {
			return nil, serr
		}
		res.Warnings = append(warnings, res.Warnings...)
		return res, nil
	}
	return &Result{Reason: "no-valid-target", Warnings: warnings}, nil
}

func indexOf(order []int, slot int) int {
	for i, n := range order {
		if n == slot {
			return i
		}
	}
	return 0
}

// Switchable reports whether a slot has both backups needed to activate it.
func Switchable(slot int, a *account.Account) bool {
	cred, unreadable := credentials.ReadBackup(slot, a.Email)
	if unreadable || cred == "" {
		return false
	}
	_, err := os.Stat(paths.AccountConfigBackup(slot, a.Email))
	return err == nil
}

// performSwitch is the locked core. It re-reads the roster under the lock,
// classifies and preserves the outgoing credential, activates the target,
// and rolls back on failure.
func performSwitch(seq *account.Sequence, target int, force bool) (*Result, error) {
	targetAcct := seq.Get(target)
	if targetAcct == nil {
		return nil, fmt.Errorf("no account in slot %d", target)
	}

	res := &Result{ToSlot: target, ToEmail: targetAcct.Email, UsedForce: force}

	// Already-active guard (pre-lock, advisory; re-checked under lock).
	liveID, idErr := claudecfg.ReadIdentity()
	if idErr == nil && liveID != nil && !force {
		if liveSlot := seq.FindByIdentity(liveID.Email, liveID.OrganizationUUID); liveSlot == target {
			live := credentials.ReadActive()
			backup, _ := credentials.ReadBackup(target, targetAcct.Email)
			if live.Value == "" || live.Value == backup ||
				oauth.Fingerprint([]byte(live.Value)) == oauth.Fingerprint([]byte(backup)) {
				res.Reason = "already-active"
				res.FromSlot, res.FromEmail = target, targetAcct.Email
				return res, nil
			}
		}
	}

	// Lock order: cswap's lock → CC credential locks → CC config lock.
	// No network I/O below this point.
	swapLock := locks.NewFileLock(paths.LockPath())
	if err := swapLock.Acquire(); err != nil {
		return nil, err
	}
	defer swapLock.Release()

	ccLocks := locks.ClaudeCredentialLocks(paths.ConfigHome())
	if err := locks.AcquireAll(ccLocks); err != nil {
		return nil, err
	}
	defer locks.ReleaseAll(ccLocks)

	cfgLock := locks.ClaudeConfigLock(paths.GlobalConfigPath())
	if err := cfgLock.Acquire(); err != nil {
		return nil, err
	}
	defer cfgLock.Release()

	// Re-read the roster under the lock.
	seq, err := account.Load()
	if err != nil {
		return nil, err
	}
	targetAcct = seq.Get(target)
	if targetAcct == nil {
		return nil, fmt.Errorf("no account in slot %d", target)
	}

	// Read target material first: nothing is mutated until both exist.
	targetCreds, unreadable := credentials.ReadBackup(target, targetAcct.Email)
	if unreadable {
		return nil, fmt.Errorf("stored credentials for Account-%d are unreadable (Keychain locked?) — retry from a GUI session; do not re-add", target)
	}
	if targetCreds == "" {
		return nil, fmt.Errorf("no stored credentials for Account-%d — re-add it with 'cswap add' while logged in as %s", target, targetAcct.Email)
	}
	targetCfg, err := os.ReadFile(paths.AccountConfigBackup(target, targetAcct.Email))
	if err != nil {
		return nil, fmt.Errorf("no config backup for Account-%d: %w", target, err)
	}

	// Snapshot live state for rollback.
	live := credentials.ReadActive()
	if live.Unreadable {
		return nil, errors.New("cannot read the live credentials; aborting before any change")
	}
	liveCfg, cfgErr := claudecfg.ReadRaw()
	if cfgErr != nil && !errors.Is(cfgErr, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot read %s; aborting: %w", paths.GlobalConfigPath(), cfgErr)
	}

	// Identify the outgoing slot under the lock.
	liveID, _ = claudecfg.ReadIdentity()
	fromSlot := 0
	if liveID != nil {
		fromSlot = seq.FindByIdentity(liveID.Email, liveID.OrganizationUUID)
	}
	if fromSlot == 0 {
		if n, a := seq.Active(); a != nil {
			fromSlot = n
		}
	}
	if from := seq.Get(fromSlot); from != nil {
		res.FromSlot, res.FromEmail = fromSlot, from.Email
	}

	// Preserve the outgoing credential. A failed backup aborts: the live
	// store must never be overwritten without a surviving copy.
	if !force && live.Value != "" && live.Value != targetCreds {
		if fromSlot != 0 && liveID != nil {
			warn, berr := backupOutgoing(seq, fromSlot, live.Value, liveCfg)
			if berr != nil {
				return nil, fmt.Errorf("could not back up the outgoing account (refusing to overwrite it): %w", berr)
			}
			if warn != "" {
				res.Warnings = append(res.Warnings, warn)
			}
		} else {
			// Unmanaged or unidentifiable live login: stash it. The stash is
			// the license to overwrite the live store.
			if err := StashCredential(live.Value, "displaced-live-login", fromSlot); err != nil {
				return nil, fmt.Errorf("could not preserve the current login (refusing to overwrite it): %w", err)
			}
			res.Warnings = append(res.Warnings, "current login was not a managed account; its credentials were stashed (see 'cswap unclaimed')")
		}
	}

	// Transaction: activate target, rolling back completed steps in reverse.
	// Rollback itself is best-effort — the caller's error message is the
	// signal when even that fails.
	var credsWritten, cfgWritten bool
	rollback := func() {
		if cfgWritten && liveCfg != nil {
			_ = claudecfg.RestoreRaw(liveCfg)
		}
		if credsWritten && live.Value != "" {
			_ = credentials.WriteActive(live.Value)
		}
	}

	merged := credentials.MergeShared(targetCreds, live.Value)
	if err := credentials.WriteActive(merged); err != nil {
		return nil, fmt.Errorf("writing live credentials: %w", err)
	}
	credsWritten = true

	if err := claudecfg.SpliceOAuthAccount(targetCfg); err != nil {
		rollback()
		return nil, fmt.Errorf("switch failed and was rolled back: %w", err)
	}
	cfgWritten = true

	seq.ActiveAccountNumber = &target
	if err := seq.Save(); err != nil {
		rollback()
		return nil, fmt.Errorf("switch failed and was rolled back: %w", err)
	}

	res.Switched = true
	res.Reason = "switched"
	return res, nil
}

// backupOutgoing classifies the live credential against the outgoing slot
// and preserves it accordingly. Returns a warning string; an error means
// the backup could not be written and the switch must abort.
func backupOutgoing(seq *account.Sequence, slot int, liveCred string, liveCfg []byte) (string, error) {
	a := seq.Get(slot)
	if a == nil {
		return "", nil
	}
	backup, unreadable := credentials.ReadBackup(slot, a.Email)

	writeCfg := func() error {
		if liveCfg == nil {
			return nil
		}
		return account.WriteFileAtomic(paths.AccountConfigBackup(slot, a.Email), liveCfg, 0o600)
	}

	// wiped: an OAuth blob whose token fields are both empty is Claude
	// Code's invalid_grant reaction — never store it over a real backup.
	if blob, err := oauth.ParseBlob([]byte(liveCred)); err == nil && blob.OAuth != nil &&
		blob.OAuth.AccessToken == "" && blob.OAuth.RefreshToken == "" {
		return fmt.Sprintf("Account-%d's live credential was wiped by Claude Code; kept the stored backup", slot), writeCfg()
	}

	if !unreadable && liveCred == backup {
		// own-bytes: nothing moved.
		return "", writeCfg()
	}
	// own-family (same refresh lineage, rotated access token) and unresolved
	// provenance both back up: fail-open, with the .prev generation as the
	// cushion if these bytes were foreign.
	if err := credentials.WriteBackup(slot, a.Email, liveCred); err != nil {
		return "", err
	}
	return "", writeCfg()
}

// Remove deletes an account: stored credentials, config backup, roster row.
func Remove(selector string) (int, string, error) {
	seq, err := account.Load()
	if err != nil {
		return 0, "", err
	}
	slot, err := seq.Resolve(selector)
	if err != nil {
		return 0, "", err
	}
	a := seq.Get(slot)
	credentials.DeleteBackup(slot, a.Email)
	os.Remove(paths.AccountConfigBackup(slot, a.Email))
	seq.Remove(slot)
	if err := seq.Save(); err != nil {
		return 0, "", err
	}
	return slot, a.Email, nil
}

// SetDisabled holds an account out of (or returns it to) auto-rotation.
func SetDisabled(selector string, disabled bool) (int, string, error) {
	seq, err := account.Load()
	if err != nil {
		return 0, "", err
	}
	slot, err := seq.Resolve(selector)
	if err != nil {
		return 0, "", err
	}
	a := seq.Get(slot)
	a.Disabled = disabled
	if !disabled {
		a.Disabled = false
	}
	if err := seq.Save(); err != nil {
		return 0, "", err
	}
	return slot, a.Email, nil
}

// SetAlias sets or clears (alias "") an account's alias.
func SetAlias(selector, alias string) (int, string, error) {
	seq, err := account.Load()
	if err != nil {
		return 0, "", err
	}
	slot, err := seq.Resolve(selector)
	if err != nil {
		return 0, "", err
	}
	a := seq.Get(slot)
	if alias == "" {
		a.Alias = ""
	} else {
		if err := account.ValidateAlias(alias); err != nil {
			return 0, "", err
		}
		if conflict := aliasConflict(seq, alias, a.Email, a.OrganizationUUID); conflict != "" {
			return 0, "", fmt.Errorf("alias %q is already used by %s", alias, conflict)
		}
		a.Alias = alias
	}
	if err := seq.Save(); err != nil {
		return 0, "", err
	}
	return slot, a.Email, nil
}

// Move relocates an account to a slot, swapping if the slot is occupied.
func Move(selector string, targetSlot int) (swapped bool, otherEmail string, err error) {
	if targetSlot < 1 {
		return false, "", errors.New("target slot must be >= 1")
	}
	seq, err := account.Load()
	if err != nil {
		return false, "", err
	}
	slot, err := seq.Resolve(selector)
	if err != nil {
		return false, "", err
	}
	if slot == targetSlot {
		return false, "", nil
	}

	lock := locks.NewFileLock(paths.LockPath())
	if err := lock.Acquire(); err != nil {
		return false, "", err
	}
	defer lock.Release()

	seq, err = account.Load()
	if err != nil {
		return false, "", err
	}
	a := seq.Get(slot)
	if a == nil {
		return false, "", fmt.Errorf("no account in slot %d", slot)
	}
	other := seq.Get(targetSlot)

	if err := relocateBackups(slot, targetSlot, a); err != nil {
		return false, "", err
	}
	if other != nil {
		if err := relocateBackups(targetSlot, slot, other); err != nil {
			return false, "", err
		}
		otherEmail = other.Email
	}

	seq.Remove(slot)
	if other != nil {
		seq.Remove(targetSlot)
		seq.Upsert(slot, other)
	}
	seq.Upsert(targetSlot, a)
	if err := seq.Save(); err != nil {
		return false, "", err
	}
	return other != nil, otherEmail, nil
}

// relocateBackups copies slot material from one key to another and removes
// the source.
func relocateBackups(from, to int, a *account.Account) error {
	cred, unreadable := credentials.ReadBackup(from, a.Email)
	if unreadable {
		return fmt.Errorf("stored credentials for slot %d are unreadable; aborting move", from)
	}
	if cred != "" {
		if err := credentials.WriteBackup(to, a.Email, cred); err != nil {
			return err
		}
	}
	if cfg, err := os.ReadFile(paths.AccountConfigBackup(from, a.Email)); err == nil {
		if werr := account.WriteFileAtomic(paths.AccountConfigBackup(to, a.Email), cfg, 0o600); werr != nil {
			return werr
		}
	}
	credentials.DeleteBackup(from, a.Email)
	os.Remove(paths.AccountConfigBackup(from, a.Email))
	return nil
}

// StashCredential preserves an unowned credential in the unclaimed store:
// an entry file (written first) plus a manifest row. Always plain files,
// never the Keychain — the abort path must not inherit Keychain failures.
func StashCredential(cred, reason string, slot int) error {
	if err := paths.EnsureDirs(); err != nil {
		return err
	}
	fp := oauth.Fingerprint([]byte(cred))
	short := strings.TrimPrefix(fp, "sha256:")
	short = strings.TrimPrefix(short, "sha256-full:")
	if len(short) > 12 {
		short = short[:12]
	}
	id := fmt.Sprintf("%s-%s-%06x", time.Now().UTC().Format("20060102T150405"), short, time.Now().UnixNano()&0xffffff)

	entryPath := paths.CredentialsDir() + "/.unclaimed-" + id + ".enc"
	if err := account.WriteFileAtomic(entryPath, []byte(base64.StdEncoding.EncodeToString([]byte(cred))), 0o600); err != nil {
		return err
	}

	manifestPath := paths.CredentialsDir() + "/.unclaimed-manifest.json"
	lock := locks.NewFileLock(paths.CredentialsDir() + "/.unclaimed-manifest.lock")
	if err := lock.Acquire(); err != nil {
		return err
	}
	defer lock.Release()

	manifest := struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Entries       map[string]map[string]any `json:"entries"`
	}{SchemaVersion: 1, Entries: map[string]map[string]any{}}
	if raw, err := os.ReadFile(manifestPath); err == nil {
		if json.Unmarshal(raw, &manifest) != nil {
			// Corrupt manifest: set it aside and start fresh.
			_ = os.Rename(manifestPath, manifestPath+".corrupt-"+fmt.Sprint(time.Now().Unix()))
			manifest.Entries = map[string]map[string]any{}
		}
	}
	manifest.Entries[id] = map[string]any{
		"createdAt":   time.Now().UTC().Format(account.TimeFormat),
		"reason":      reason,
		"configSlot":  fmt.Sprint(slot),
		"fingerprint": fp,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return account.WriteFileAtomic(manifestPath, data, 0o600)
}
