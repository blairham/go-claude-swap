package switcher

import (
	"math"
	"net/http"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// UsageStatus classifies why usage is (un)available for an account.
type UsageStatus string

// UsageStatus values.
const (
	StatusOK                  UsageStatus = "ok"
	StatusTokenExpired        UsageStatus = "token_expired"
	StatusAPIKey              UsageStatus = "api_key"
	StatusKeychainUnavailable UsageStatus = "keychain_unavailable"
	StatusReloginRequired     UsageStatus = "relogin_required"
	StatusNoCredentials       UsageStatus = "no_credentials"
	StatusUnavailable         UsageStatus = "unavailable"
)

// Snapshot is one account's assembled view for list/status/TUI/auto.
type Snapshot struct {
	Slot     int
	Account  *account.Account
	Active   bool
	Status   UsageStatus
	Usage    *usage.Usage // decision-grade (nil when stale)
	LastGood *usage.Usage // stale fallback for display
	Age      float64      // seconds since last good fetch; +Inf if never
	LastErr  string
}

// Collector fetches usage across accounts, respecting the store's cadence
// and the endpoint's request budget.
type Collector struct {
	Client *http.Client
	// StoreOnly reads cached rows without any network fetch.
	StoreOnly bool
	// Models folds per-model weekly windows into headroom.
	Models []string
}

// Collect assembles snapshots for every account. The active account's token
// is never refreshed (Claude Code owns it); inactive accounts are refreshed
// when expired, and a permanent refresh failure quarantines the slot.
func (c *Collector) Collect(seq *account.Sequence) []Snapshot {
	now := time.Now()
	client := c.Client
	if client == nil {
		client = http.DefaultClient
	}

	activeSlot := 0
	if id, err := claudecfg.ReadIdentity(); err == nil && id != nil {
		activeSlot = seq.FindByIdentity(id.Email, id.OrganizationUUID)
	}
	if activeSlot == 0 {
		if n, a := seq.Active(); a != nil {
			activeSlot = n
		}
	}

	store := usage.LoadStore()
	var out []Snapshot
	for _, slot := range seq.Order {
		a := seq.Get(slot)
		if a == nil {
			continue
		}
		snap := Snapshot{Slot: slot, Account: a, Active: slot == activeSlot}
		c.fill(&snap, store, client, now)
		out = append(out, snap)
	}
	return out
}

func (c *Collector) fill(snap *Snapshot, store *usage.Store, client *http.Client, now time.Time) {
	a := snap.Account
	entry := store.Get(snap.Slot, a.Email, a.OrganizationUUID)
	if entry != nil {
		snap.Age = entry.Age(now)
		snap.LastGood = entry.LastGood
		snap.LastErr = entry.LastError
	} else {
		snap.Age = math.Inf(1)
	}

	// Resolve the credential for this slot.
	var cred string
	var unreadable bool
	if snap.Active {
		act := credentials.ReadActive()
		cred, unreadable = act.Value, act.Unreadable
	} else {
		cred, unreadable = credentials.ReadBackup(snap.Slot, a.Email)
	}

	switch {
	case unreadable:
		snap.Status = StatusKeychainUnavailable
		return
	case cred == "":
		snap.Status = StatusNoCredentials
		return
	case credentials.IsAPIKey(cred):
		snap.Status = StatusAPIKey
		return
	}

	fp := oauth.Fingerprint([]byte(cred))
	if entry != nil && entry.TokenDead(fp) {
		snap.Status = StatusReloginRequired
		return
	}

	if entry != nil && entry.Fresh(now) {
		snap.Status = StatusOK
		snap.Usage = entry.LastGood
		return
	}
	if c.StoreOnly || (entry != nil && (entry.InBackoff(now) || !entry.PollDue(now))) {
		c.finishFromCache(snap, entry, now)
		return
	}

	blob, err := oauth.ParseBlob([]byte(cred))
	if err != nil || blob.OAuth == nil || blob.OAuth.AccessToken == "" {
		snap.Status = StatusUnavailable
		return
	}

	// Refresh an expired inactive token before the usage call. The active
	// account is Claude Code's to refresh.
	if !snap.Active && oauth.Expired(blob.OAuth.ExpiresAt, now) && blob.OAuth.RefreshToken != "" {
		outcome := oauth.Refresh(client, []byte(cred), time.Now)
		switch {
		case outcome.Err == oauth.ErrNone:
			if newRaw, merr := oauth.MarshalBlob(outcome.Blob); merr == nil {
				// Best-effort persist: even if the write fails, the fetch
				// below uses the successor we hold in memory.
				_ = credentials.WriteBackup(snap.Slot, a.Email, string(newRaw))
				cred = string(newRaw)
				blob = outcome.Blob
			}
		case outcome.Err.Permanent():
			c.recordAuthDead(snap, fp, now)
			snap.Status = StatusReloginRequired
			return
		}
		// Transient refresh failure: try the expired token anyway.
	}

	u, ferr := usage.Fetch(client, blob.OAuth.AccessToken)
	if ferr != nil && ferr.HTTPStatus == 401 && !snap.Active && blob.OAuth.RefreshToken != "" {
		// One refresh + one retry on a 401.
		outcome := oauth.Refresh(client, []byte(cred), time.Now)
		if outcome.Err == oauth.ErrNone {
			if newRaw, merr := oauth.MarshalBlob(outcome.Blob); merr == nil {
				_ = credentials.WriteBackup(snap.Slot, a.Email, string(newRaw))
			}
			u, ferr = usage.Fetch(client, outcome.Blob.OAuth.AccessToken)
		} else if outcome.Err.Permanent() {
			c.recordAuthDead(snap, fp, now)
			snap.Status = StatusReloginRequired
			return
		}
	}

	if ferr != nil {
		if snap.Active && (ferr.HTTPStatus == 401 || oauth.Expired(blob.OAuth.ExpiresAt, now)) {
			snap.Status = StatusTokenExpired
		} else {
			snap.Status = StatusUnavailable
			snap.LastErr = ferr.Kind
		}
		c.persistFailure(snap, ferr, now)
		return
	}

	snap.Status = StatusOK
	snap.Usage = u
	snap.LastGood = u
	snap.Age = 0
	c.persistSuccess(snap, u, now)
}

func (c *Collector) finishFromCache(snap *Snapshot, entry *usage.Entry, now time.Time) {
	if entry != nil {
		if dv := entry.DecisionValue(now, c.Models); dv != nil {
			snap.Status = StatusOK
			snap.Usage = dv
			return
		}
	}
	snap.Status = StatusUnavailable
}

func (c *Collector) persistSuccess(snap *Snapshot, u *usage.Usage, now time.Time) {
	a := snap.Account
	_ = usage.Update(func(s *usage.Store) {
		e := s.Get(snap.Slot, a.Email, a.OrganizationUUID)
		prevPct := math.NaN()
		var prevIv float64
		if e != nil {
			if e.LastGood != nil {
				if h, ok := e.LastGood.Headroom(c.Models); ok {
					prevPct = 100 - h
				}
			}
			if e.PollIntervalS != nil {
				prevIv = *e.PollIntervalS
			}
		} else {
			e = &usage.Entry{Email: a.Email, OrganizationUUID: a.OrganizationUUID}
		}
		recent429 := e.Recent429(now)
		e.RecordSuccess(u, now)

		newPct := math.NaN()
		exhausted := false
		if h, ok := u.Headroom(c.Models); ok {
			newPct = 100 - h
			exhausted = h <= 0
		}
		next, iv := usage.PlanAfterFetch(usage.PlanInput{
			Active:       snap.Active,
			PrevInterval: prevIv,
			PrevPct:      prevPct,
			NewPct:       newPct,
			Threshold:    90,
			Recent429:    recent429,
			Exhausted:    exhausted,
			EarliestRst:  u.EarliestFutureReset(c.Models, now),
			LimitingRst:  u.LatestExhaustedReset(c.Models),
		}, now)
		e.NextPollAt = &next
		e.PollIntervalS = &iv
		s.Put(snap.Slot, e)
	})
}

func (c *Collector) persistFailure(snap *Snapshot, ferr *usage.FetchError, now time.Time) {
	a := snap.Account
	_ = usage.Update(func(s *usage.Store) {
		e := s.Get(snap.Slot, a.Email, a.OrganizationUUID)
		if e == nil {
			e = &usage.Entry{Email: a.Email, OrganizationUUID: a.OrganizationUUID}
		}
		e.RecordFailure(ferr, now)
		s.Put(snap.Slot, e)
	})
}

func (c *Collector) recordAuthDead(snap *Snapshot, fingerprint string, now time.Time) {
	a := snap.Account
	_ = usage.Update(func(s *usage.Store) {
		e := s.Get(snap.Slot, a.Email, a.OrganizationUUID)
		if e == nil {
			e = &usage.Entry{Email: a.Email, OrganizationUUID: a.OrganizationUUID}
		}
		e.RecordAuthDead(fingerprint, now)
		s.Put(snap.Slot, e)
	})
}
