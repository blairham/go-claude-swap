package usage

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// Store constants (all seconds unless noted).
const (
	// StoreSchemaVersion of cache/usage.json.
	StoreSchemaVersion = 2
	// ServeTTL: fresher than this → serve from store, no fetch.
	ServeTTL = 180.0
	// StaleOK: measurements older than this are not decision-grade.
	StaleOK = 300.0
	// TrustMaxAge is the general decision-trust ceiling.
	TrustMaxAge = 3600.0
	// RateLimitTrustMaxAge is the 429-stale fallback ceiling: usage is
	// monotone within a window, so a 429-frozen lastGood stays a valid
	// lower bound until the window resets.
	RateLimitTrustMaxAge = 7200.0

	backoffBase     = 30.0
	backoffCap      = 600.0
	retryAfterMax   = 4500.0
	retryAfterMarg  = 900.0
	edgeBackoff     = 300.0
	nonRateLimitCap = 3600.0

	// AuthDeadStrikes: refresh failures before a slot is quarantined.
	AuthDeadStrikes = 1
)

// Entry is one account's row in the usage store.
type Entry struct {
	Email               string   `json:"email"`
	OrganizationUUID    string   `json:"organizationUuid"`
	LastGood            *Usage   `json:"lastGood"`
	FetchedAt           float64  `json:"fetchedAt,omitempty"` // success only
	LastAttemptAt       float64  `json:"lastAttemptAt,omitempty"`
	ConsecutiveFailures int      `json:"consecutiveFailures,omitempty"`
	LastError           string   `json:"lastError,omitempty"`
	BackoffUntil        *float64 `json:"backoffUntil,omitempty"`
	NextPollAt          *float64 `json:"nextPollAt,omitempty"`
	PollIntervalS       *float64 `json:"pollIntervalS,omitempty"`
	Last429At           *float64 `json:"last429At,omitempty"` // never cleared by success
	AuthDeadStrikes     int      `json:"authDeadStrikes,omitempty"`
	StruckFingerprint   string   `json:"struckFingerprint,omitempty"`
}

// Store is the on-disk usage cache, keyed by slot number.
type Store struct {
	SchemaVersion int               `json:"schemaVersion"`
	Accounts      map[string]*Entry `json:"accounts"`
}

// LoadStore reads cache/usage.json; a missing or wrong-version file is empty.
func LoadStore() *Store {
	s := &Store{SchemaVersion: StoreSchemaVersion, Accounts: map[string]*Entry{}}
	raw, err := os.ReadFile(paths.UsageStorePath())
	if err != nil {
		return s
	}
	var loaded Store
	if json.Unmarshal(raw, &loaded) != nil || loaded.SchemaVersion != StoreSchemaVersion {
		return s
	}
	if loaded.Accounts == nil {
		loaded.Accounts = map[string]*Entry{}
	}
	return &loaded
}

// Save writes the store atomically under the store lock.
func (s *Store) Save() error {
	lock := locks.NewFileLock(paths.CacheDir() + "/.usage.lock")
	if err := lock.Acquire(); err != nil {
		return err
	}
	defer lock.Release()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return account.WriteFileAtomic(paths.UsageStorePath(), data, 0o600)
}

// Update applies fn to the store under the lock (read-modify-write).
func Update(fn func(*Store)) error {
	lock := locks.NewFileLock(paths.CacheDir() + "/.usage.lock")
	if err := lock.Acquire(); err != nil {
		return err
	}
	defer lock.Release()
	s := LoadStore()
	fn(s)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return account.WriteFileAtomic(paths.UsageStorePath(), data, 0o600)
}

// Get returns the row for a slot only when it matches the account's
// identity (slot reuse must never serve the previous account's data).
func (s *Store) Get(slot int, email, orgUUID string) *Entry {
	e := s.Accounts[strconv.Itoa(slot)]
	if e == nil || e.Email != email || e.OrganizationUUID != orgUUID {
		return nil
	}
	return e
}

// Put replaces a slot's row.
func (s *Store) Put(slot int, e *Entry) {
	s.Accounts[strconv.Itoa(slot)] = e
}

// Age returns seconds since the last successful fetch, or +Inf.
func (e *Entry) Age(now time.Time) float64 {
	if e == nil || e.FetchedAt == 0 {
		return math.Inf(1)
	}
	return float64(now.Unix()) - e.FetchedAt
}

// Fresh reports whether the row can be served without a fetch.
func (e *Entry) Fresh(now time.Time) bool {
	return e.Age(now) <= ServeTTL
}

// DecisionValue returns usage suitable for switch decisions: lastGood only
// while decision-trusted, else nil.
func (e *Entry) DecisionValue(now time.Time, models []string) *Usage {
	if e == nil || e.LastGood == nil {
		return nil
	}
	age := e.Age(now)
	if age <= StaleOK {
		return e.LastGood
	}
	// Trust extension: rows in failure/backoff keep their lastGood up to a
	// ceiling; a 429-frozen row is trusted until the window resets.
	ceiling := TrustMaxAge
	if e.LastError == "http-429" {
		ceiling = RateLimitTrustMaxAge
		if reset := e.LastGood.EarliestFutureReset(models, now); reset > 0 {
			if until := float64(reset) - e.FetchedAt; until < ceiling {
				ceiling = until
			}
		}
	}
	inBackoff := e.BackoffUntil != nil && float64(now.Unix()) < *e.BackoffUntil
	planned := e.NextPollAt != nil && float64(now.Unix()) < *e.NextPollAt
	if age <= ceiling && (e.ConsecutiveFailures > 0 || inBackoff || planned) {
		return e.LastGood
	}
	return nil
}

// TokenDead reports whether the slot is quarantined for the given stored
// credential fingerprint. A rewritten credential (fingerprint mismatch)
// heals the strike.
func (e *Entry) TokenDead(storedFingerprint string) bool {
	if e == nil || e.AuthDeadStrikes < AuthDeadStrikes {
		return false
	}
	if e.StruckFingerprint == "" {
		return true
	}
	return e.StruckFingerprint == storedFingerprint
}

// RecordSuccess stores a fresh measurement and clears failure state.
func (e *Entry) RecordSuccess(u *Usage, now time.Time) {
	e.LastGood = u
	e.FetchedAt = float64(now.Unix())
	e.LastAttemptAt = e.FetchedAt
	e.ConsecutiveFailures = 0
	e.LastError = ""
	e.BackoffUntil = nil
	e.AuthDeadStrikes = 0
	e.StruckFingerprint = ""
	// Last429At deliberately survives success: post-429 pacing keys off it.
}

// RecordFailure stores a failed attempt and computes backoff.
func (e *Entry) RecordFailure(fe *FetchError, now time.Time) {
	e.LastAttemptAt = float64(now.Unix())
	e.ConsecutiveFailures++
	e.LastError = fe.Kind
	rateLimited := fe.HTTPStatus == 429
	if rateLimited {
		t := float64(now.Unix())
		e.Last429At = &t
	}
	until := float64(now.Unix()) + failureBackoff(e.ConsecutiveFailures, fe.RetryAfter, rateLimited)
	e.BackoffUntil = &until
}

// RecordAuthDead registers a permanent refresh failure against the consumed
// credential's fingerprint.
func (e *Entry) RecordAuthDead(fingerprint string, now time.Time) {
	e.LastAttemptAt = float64(now.Unix())
	e.AuthDeadStrikes++
	e.StruckFingerprint = fingerprint
}

// ClearDeadToken lifts the quarantine (called on any credential write).
func (e *Entry) ClearDeadToken() {
	e.AuthDeadStrikes = 0
	e.StruckFingerprint = ""
}

// InBackoff reports whether fetches are currently parked.
func (e *Entry) InBackoff(now time.Time) bool {
	return e != nil && e.BackoffUntil != nil && float64(now.Unix()) < *e.BackoffUntil
}

// failureBackoff computes the retry delay: exponential 30s→600s, honoring
// Retry-After with a safety margin on long 429 parks (retries landing
// exactly on the deadline re-block most of the time for a fresh hour).
func failureBackoff(failures int, retryAfter float64, rateLimited bool) float64 {
	shift := min(failures-1, 32)
	computed := math.Min(backoffBase*math.Pow(2, float64(shift)), backoffCap)
	if retryAfter == 0 && !rateLimited {
		return computed
	}
	if retryAfter == 0 && rateLimited {
		// Retry-After: 0 on a 429 is the saturated-budget edge.
		return math.Min(math.Max(computed, edgeBackoff), backoffCap)
	}
	asked := retryAfter
	if asked > backoffCap && rateLimited {
		asked += retryAfterMarg
	}
	bound := nonRateLimitCap
	if rateLimited {
		bound = retryAfterMax
	}
	asked = math.Min(asked, bound)
	return math.Max(asked, computed)
}

// PruneMismatched drops rows whose identity no longer matches the roster
// (slot was reused).
func (s *Store) PruneMismatched(identities map[int][2]string) {
	for key := range s.Accounts {
		slot, err := strconv.Atoi(key)
		if err != nil {
			delete(s.Accounts, key)
			continue
		}
		id, ok := identities[slot]
		if !ok {
			continue // keep rows for removed slots; import auto-heal uses them
		}
		e := s.Accounts[key]
		if e.Email != id[0] || e.OrganizationUUID != id[1] {
			delete(s.Accounts, key)
		}
	}
}

// ErrClaimBusy is reserved for future claim/fencing support.
var ErrClaimBusy = errors.New("usage fetch already in flight")
