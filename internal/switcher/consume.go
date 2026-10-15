// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package switcher

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// Gate outcomes beside oauth's own kinds. Both are local conditions that
// clear without the account being at fault, so neither ever strikes.
const (
	// ErrConsumeBusy: another gate holds the slot's consume lock and will
	// finish; this pass yields rather than POST the same grant.
	ErrConsumeBusy oauth.ErrorKind = "consume-busy"
	// ErrStashUnreadable: a stashed successor for the slot's current
	// generation exists but cannot be read. POSTing instead would spend a
	// generation that successor already superseded.
	ErrStashUnreadable oauth.ErrorKind = "stash-unreadable"
)

// Stash reasons the gate writes. consumedFp in the manifest row is the
// adoption key: the generation the stashed successor superseded.
const (
	stashPersistFailed     = "consume-gate-persist-failed"
	stashPersistLockFailed = "consume-gate-persist-lock-failed"
	stashUnpersisted       = "consume-gate-unpersisted"
	stashStoreUnreadable   = "consume-gate-store-unreadable"
	stashSlotRemoved       = "consume-gate-slot-removed"
	stashCASConflict       = "consume-gate-cas-conflict"
)

// demotingStash reports whether a stash reason means the slot still holds
// the generation whose grant was just spent. A removed slot has nothing to
// activate, and a CAS conflict left the slot on a racing writer's newer
// lineage — freshened, which is what a demotion would deny.
func demotingStash(reason string) bool {
	switch reason {
	case stashPersistFailed, stashPersistLockFailed, stashUnpersisted, stashStoreUnreadable:
		return true
	}
	return false
}

// GrantOutcome is the result of ConsumeGrant.
type GrantOutcome struct {
	// Cred is the slot's now-current credential: the refreshed successor, a
	// racing writer's newer lineage, or an adopted one. Empty when the gate
	// deferred before any POST.
	Cred string
	// Err is oauth's refresh failure unchanged, a gate kind, or transient
	// when a spent grant's successor could not be persisted (Cred still
	// carries it, so a caller needing a token for this request keeps one).
	Err oauth.ErrorKind
	// ConsumedFP is the fingerprint of the bytes actually POSTed, which may
	// differ from the caller's snapshot. Strikes bind to it.
	ConsumedFP string
	// Stashed is set on a demoted outcome whose successor was parked for
	// the next gate pass to adopt. False with Err transient and Cred set is
	// the one unrecoverable corner: neither persisted nor stashed.
	Stashed bool
}

// gateLockTimeout bounds the gate's waits on cswap's lock; a variable so
// tests can exercise the lock-unavailable paths without a 10s wait.
var gateLockTimeout = 10 * time.Second

// consumeLockPath is shared with the original claude-swap, so the two tools
// serialize against each other too.
func consumeLockPath(slot int) string {
	return filepath.Join(paths.CredentialsDir(), fmt.Sprintf(".consume-%d.lock", slot))
}

// ConsumeGrant is the gate through which a backup refresh token is spent.
// A refresh token is one-time-use, so the POST consumes the provably
// freshest copy of the slot's grant, never the caller's snapshot, which may
// be a superseded generation.
//
// The sequence runs under the slot's consume lock, which only other gates
// contend on: re-read under cswap's lock (adopting a stashed successor or a
// quiescent session profile's newer generation), POST outside it, then
// compare-and-swap on the refresh-token fingerprint and either persist the
// successor or stash it. A consumed generation is never discarded. The
// caller must not hold cswap's lock (it is not re-entrant).
func ConsumeGrant(client *http.Client, slot int, email, snapshot string) GrantOutcome {
	if client == nil {
		client = RefreshClient
	}
	if err := paths.EnsureDirs(); err != nil {
		return GrantOutcome{Err: oauth.ErrTransient}
	}
	consume := locks.NewFileLock(consumeLockPath(slot))
	if err := consume.Acquire(); err != nil {
		return GrantOutcome{Err: ErrConsumeBusy}
	}
	defer consume.Release()
	return consumeLocked(client, slot, email, snapshot)
}

func consumeLocked(client *http.Client, slot int, email, snapshot string) GrantOutcome {
	input, kind := gateInput(slot, email)
	if kind != oauth.ErrNone {
		return GrantOutcome{Err: kind}
	}
	consumedFP := oauth.Fingerprint([]byte(input))

	// The world already moved past the caller's snapshot and the current
	// generation is fresh: the refresh the caller wanted has effectively
	// happened (a racing gate's rotation, an adopted stash or profile).
	// When the re-read equals the snapshot — the 401-retry shape, the
	// server just rejected these bytes — this never fires.
	if in, err := oauth.ParseBlob([]byte(input)); err == nil && in.OAuth != nil && in.OAuth.AccessToken != "" {
		if sn, err := oauth.ParseBlob([]byte(snapshot)); err == nil && sn.OAuth != nil &&
			sn.OAuth.AccessToken != "" && sn.OAuth.AccessToken != in.OAuth.AccessToken &&
			!oauth.Expired(in.OAuth.ExpiresAt, time.Now()) {
			return GrantOutcome{Cred: input, ConsumedFP: consumedFP}
		}
	}

	outcome := oauth.Refresh(client, []byte(input), time.Now)
	if outcome.Err != oauth.ErrNone || outcome.Blob == nil {
		kind := outcome.Err
		if kind == oauth.ErrNone {
			kind = oauth.ErrTransient
		}
		return GrantOutcome{Err: kind, ConsumedFP: consumedFP}
	}
	raw, err := oauth.MarshalBlob(outcome.Blob)
	if err != nil {
		// Unreachable for a blob Refresh just built; the grant is spent
		// either way and there are no bytes to keep.
		return GrantOutcome{Err: oauth.ErrTransient, ConsumedFP: consumedFP}
	}
	successor := string(raw)

	cred, reason := persistSuccessor(slot, email, successor, consumedFP)
	if demotingStash(reason) {
		// The successor is parked, not persisted: activating the slot now
		// would install a generation whose grant is spent. Report
		// transient so the caller defers; the next pass adopts the stash.
		return GrantOutcome{Cred: cred, Err: oauth.ErrTransient, ConsumedFP: consumedFP, Stashed: reason != stashUnpersisted}
	}
	return GrantOutcome{Cred: cred, ConsumedFP: consumedFP}
}

// gateInput re-reads the slot's freshest generation under cswap's lock:
// the backup, advanced first by a stashed successor whose persist a prior
// gate could not finish, then by a quiescent session profile that rotated
// past it. Nothing is POSTed here, so every failure defers.
func gateInput(slot int, email string) (string, oauth.ErrorKind) {
	lock := locks.NewFileLock(paths.LockPath())
	lock.Timeout = gateLockTimeout
	if err := lock.Acquire(); err != nil {
		return "", oauth.ErrTransient
	}
	defer lock.Release()

	current, unreadable := credentials.ReadBackup(slot, email)
	if unreadable {
		return "", oauth.ErrTransient
	}
	if current == "" {
		// Absent, not unreadable: the slot was removed after the caller
		// read its snapshot. Spending a grant for it would stash a
		// successor keyed to a generation no slot holds.
		return "", oauth.ErrTransient
	}
	adopted, err := adoptStashedSuccessor(slot, email, current)
	if errors.Is(err, errStashUnreadable) {
		return "", ErrStashUnreadable
	}
	if err != nil {
		return "", oauth.ErrTransient
	}
	if adopted != "" {
		current = adopted
	}
	seq, err := account.Load()
	if err != nil {
		return "", oauth.ErrTransient
	}
	if a := seq.Get(slot); a != nil {
		if ok, err := adoptLocked(slot, email, a.OrganizationUUID); err == nil && ok {
			if cur, unr := credentials.ReadBackup(slot, email); !unr && cur != "" {
				current = cur
			}
		}
	}
	return current, oauth.ErrNone
}

// persistSuccessor writes a refreshed successor back when the slot still
// holds the generation that was consumed, and stashes it otherwise. It
// returns the slot's now-current credential and the stash reason ("" when
// the successor was persisted). It never fails: the grant is spent.
func persistSuccessor(slot int, email, successor, consumedFP string) (string, string) {
	stash := func(reason string) string {
		if err := stashCredential(successor, reason, slot, map[string]any{"consumedFp": consumedFP}); err != nil {
			return stashUnpersisted
		}
		return reason
	}

	lock := locks.NewFileLock(paths.LockPath())
	lock.Timeout = gateLockTimeout
	if err := lock.Acquire(); err != nil {
		// The successor works; the next pass adopts it from the stash.
		return successor, stash(stashPersistLockFailed)
	}
	defer lock.Release()

	now, unreadable := credentials.ReadBackup(slot, email)
	switch {
	case unreadable:
		// The CAS cannot be evaluated: writing could clobber a racing
		// writer, and the slot may still hold the spent generation.
		return successor, stash(stashStoreUnreadable)
	case now == "":
		// Removed mid-POST: writing back would resurrect a deleted slot.
		return successor, stash(stashSlotRemoved)
	case oauth.Fingerprint([]byte(now)) != consumedFP:
		// A writer replaced the lineage while the POST was in flight: its
		// newer credential is the slot's, ours is parked.
		return now, stash(stashCASConflict)
	}
	if err := credentials.WriteBackup(slot, email, successor); err != nil {
		return successor, stash(stashPersistFailed)
	}
	return successor, ""
}

// stashManifest is the unclaimed manifest as the gate reads it.
type stashManifest struct {
	Entries map[string]struct {
		Reason     string `json:"reason"`
		ConfigSlot string `json:"configSlot"`
		ConsumedFP string `json:"consumedFp"`
	} `json:"entries"`
}

var errStashUnreadable = errors.New("stashed successor unreadable")

// adoptStashedSuccessor completes a prior gate's failed persist: when the
// slot still stores exactly the generation a stash row superseded, that
// row holds its live successor, so writing it back is the pending persist.
// Rows that can never be adopted are retired on the way. It returns the
// adopted credential, "" when nothing applied, errStashUnreadable when a
// matching row (or the manifest itself, with entry files present) cannot
// be read, or the write error when adopting failed. Caller holds cswap's
// lock.
func adoptStashedSuccessor(slot int, email, current string) (string, error) {
	curFP := oauth.Fingerprint([]byte(current))
	if curFP == "" {
		return "", nil
	}
	scan, err := scanStash(slot, curFP)
	if err != nil {
		return "", err
	}
	if scan.cred != "" {
		if err := credentials.WriteBackup(slot, email, scan.cred); err != nil {
			// Falling through would POST the spent generation this row
			// superseded; defer instead.
			return "", err
		}
		scan.retire = append(scan.retire, scan.id)
	}
	retireStashRows(scan.retire)
	return scan.cred, nil
}

// retireStashRows drops rows no pass could ever adopt. Housekeeping, never
// fatal: a row left behind can no longer match and is retired on a later
// pass. It stops at the first failure — the manifest is not accepting
// writes right now.
func retireStashRows(ids []string) {
	for _, id := range ids {
		if PurgeUnclaimed(id) != nil {
			return
		}
	}
}

// stashScan is what adoptStashedSuccessor found for one slot.
type stashScan struct {
	id     string   // the adoptable row
	cred   string   // its credential; "" when no row applies
	retire []string // rows no pass could ever adopt
}

// scanStash looks for a readable successor of curFP among the slot's stash
// rows, collecting the rows that can never be adopted: a CAS conflict's
// (the store moved past its generation for good) and any whose bytes are
// gone. A matching row that is unreadable right now does not stop the scan
// — a readable sibling on the same generation may follow — but with none,
// the result is errStashUnreadable: each such row is the sole copy of a
// generation some pass already spent.
func scanStash(slot int, curFP string) (stashScan, error) {
	var out stashScan
	manifest, verdict := readStashManifest()
	if verdict == manifestUnreadable || (verdict == manifestCorrupt && stashEntryFilesExist()) {
		return out, errStashUnreadable
	}
	ids := make([]string, 0, len(manifest.Entries))
	for id, row := range manifest.Entries {
		if row.ConfigSlot == fmt.Sprint(slot) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)

	deferred := false
	for _, id := range ids {
		row := manifest.Entries[id]
		cred, unreadable := readStashEntry(id)
		switch {
		case row.ConsumedFP != curFP:
			if row.Reason == stashCASConflict || (cred == "" && !unreadable) {
				out.retire = append(out.retire, id)
			}
		case unreadable:
			deferred = true
		case cred == "":
			out.retire = append(out.retire, id)
		default:
			out.id, out.cred = id, cred
			return out, nil
		}
	}
	if deferred {
		return out, errStashUnreadable
	}
	return out, nil
}

const (
	manifestOK = iota
	manifestUnreadable
	manifestCorrupt
)

func stashManifestPath() string {
	return filepath.Join(paths.CredentialsDir(), ".unclaimed-manifest.json")
}

func stashEntryPath(id string) string {
	return filepath.Join(paths.CredentialsDir(), ".unclaimed-"+id+".enc")
}

// readStashManifest distinguishes an absent manifest (nothing stashed)
// from one that exists but cannot be read or parsed.
func readStashManifest() (stashManifest, int) {
	var m stashManifest
	raw, err := os.ReadFile(stashManifestPath())
	if errors.Is(err, fs.ErrNotExist) {
		return m, manifestOK
	}
	if err != nil {
		return m, manifestUnreadable
	}
	if json.Unmarshal(raw, &m) != nil {
		return stashManifest{}, manifestCorrupt
	}
	return m, manifestOK
}

func stashEntryFilesExist() bool {
	files, err := filepath.Glob(filepath.Join(paths.CredentialsDir(), ".unclaimed-*.enc"))
	return err == nil && len(files) > 0
}

// readStashEntry returns an entry's credential. ("", false) is absent or
// undecodable — gone for good; ("", true) is unreadable right now.
func readStashEntry(id string) (string, bool) {
	raw, err := os.ReadFile(stashEntryPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		return "", true
	}
	dec, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return "", false
	}
	return string(dec), false
}
