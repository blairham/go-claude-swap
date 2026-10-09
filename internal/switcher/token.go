package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// setupTokenScope is the only scope a `claude setup-token` token carries:
// setup-tokens are inference-only server-side, and recording wider scopes
// makes Claude Code attempt calls that answer 403.
const setupTokenScope = "user:inference"

// TokenResult reports a completed AddToken.
type TokenResult struct {
	Slot      int
	Email     string
	APIKey    bool // the token was a managed API key, not a setup-token
	Updated   bool // an existing token account was refreshed in place
	MovedFrom int  // the identity was relocated from this slot (0: not moved)
}

// AddToken registers a raw OAuth setup-token (from `claude setup-token`) or
// a managed API key (sk-ant-api…) as an account, without a Claude Code login
// on this machine and without any network call. email "" synthesizes a
// placeholder (setup-token-N@token.local / api-key-N@token.local), since
// these tokens carry no identity metadata; slot 0 means auto-assign.
//
// Storage matches claude-swap's add-token byte for byte: a setup-token is
// wrapped as {"claudeAiOauth": {"accessToken": …, "scopes": ["user:inference"]}},
// an API key is stored raw, the config backup is a bare personal oauthAccount,
// and an API-key roster row carries "kind": "api_key".
func AddToken(token, email string, slot int) (*TokenResult, error) {
	if err := refuseSessionShell(); err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("token cannot be empty")
	}
	if slot < 0 {
		return nil, errors.New("slot number must be >= 1")
	}
	if email != "" && !emailRe.MatchString(email) {
		return nil, fmt.Errorf("invalid email format: %s", email)
	}
	isAPIKey := credentials.IsAPIKey(token)
	if err := paths.EnsureDirs(); err != nil {
		return nil, err
	}

	lock := locks.NewFileLock(paths.LockPath())
	if err := lock.Acquire(); err != nil {
		return nil, err
	}
	defer lock.Release()

	seq, err := account.Load()
	if err != nil {
		return nil, err
	}

	// A placeholder email is keyed by slot, so a synthesized account always
	// lands in the slot it is named for.
	if email == "" {
		if slot == 0 {
			slot = seq.NextSlot()
		}
		label := "setup-token"
		if isAPIKey {
			label = "api-key"
		}
		email = fmt.Sprintf("%s-%d@token.local", label, slot)
	}

	// Identity is (email, org) only, so an OAuth and an API-key account that
	// share an email could not be told apart at switch time: refuse instead
	// of silently converting one into the other.
	existing := seq.FindByIdentity(email, "")
	if existing != 0 && isAPIKeyAccount(seq.Get(existing)) != isAPIKey {
		kinds := map[bool]string{true: "API-key", false: "OAuth"}
		return nil, fmt.Errorf("%q already exists as an %s account (slot %d); cannot add it as an %s account — pass a distinct --email",
			email, kinds[!isAPIKey], existing, kinds[isAPIKey])
	}

	cred, cfg := tokenMaterial(token, email, isAPIKey)
	res := &TokenResult{Email: email, APIKey: isAPIKey}

	target := slot
	switch {
	case existing != 0 && (slot == 0 || slot == existing):
		// Same identity, no relocation asked for: refresh in place.
		target, res.Updated = existing, true
	case slot != 0:
		if other := seq.Get(slot); other != nil && (other.Email != email || other.OrganizationUUID != "") {
			return nil, fmt.Errorf("slot %d is occupied by %s (remove it first)", slot, other.Email)
		}
		res.MovedFrom = existing
	default:
		target = seq.NextSlot()
	}
	res.Slot = target

	if err := credentials.WriteBackup(target, email, cred); err != nil {
		return nil, fmt.Errorf("writing credential backup: %w", err)
	}
	if err := account.WriteFileAtomic(paths.AccountConfigBackup(target, email), cfg, 0o600); err != nil {
		return nil, fmt.Errorf("writing config backup: %w", err)
	}
	// A fresh token lifts any dead-token strike the slot carried.
	_ = usage.Update(func(s *usage.Store) {
		if e := s.Get(target, email, ""); e != nil {
			e.ClearDeadToken()
		}
	})

	if res.Updated {
		return res, seq.Save()
	}

	var prior *account.Account
	if res.MovedFrom != 0 {
		// Relocation keeps the identity, so its mappings stay valid.
		prior = seq.Get(res.MovedFrom)
		credentials.DeleteBackup(res.MovedFrom, email)
		os.Remove(paths.AccountConfigBackup(res.MovedFrom, email))
		seq.Remove(res.MovedFrom)
	}
	rec := &account.Account{Email: email, Added: time.Now().UTC().Format(account.TimeFormat)}
	if prior != nil {
		rec.Alias, rec.Disabled = prior.Alias, prior.Disabled
	}
	if isAPIKey {
		rec.Kind = account.KindAPIKey
	}
	seq.Upsert(target, rec)
	if err := seq.Save(); err != nil {
		return nil, err
	}
	return res, nil
}

// isAPIKeyAccount reports a roster row's kind; rows without one are OAuth.
func isAPIKeyAccount(a *account.Account) bool {
	return a != nil && a.Kind == account.KindAPIKey
}

// tokenMaterial builds the credential and config backups for a token
// account in the exact bytes claude-swap writes (Python json.dumps with
// default separators), so both tools fingerprint a setup-token alike.
func tokenMaterial(token, email string, isAPIKey bool) (cred string, cfg []byte) {
	quote := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	if isAPIKey {
		cred = token
	} else {
		cred = `{"claudeAiOauth": {"accessToken": ` + quote(token) + `, "scopes": ["` + setupTokenScope + `"]}}`
	}
	cfg = []byte(`{"oauthAccount": {"emailAddress": ` + quote(email) +
		`, "accountUuid": "", "organizationUuid": null, "organizationName": null}}`)
	return cred, cfg
}
