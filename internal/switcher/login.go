package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// LoginResult reports where a fresh login landed.
type LoginResult struct {
	Slot      int
	Email     string
	Created   bool // a new roster row was added
	Activated bool // the account was live, so the live credential was replaced too
}

// StoreLogin persists a credential freshly obtained from the OAuth flow into
// the slot matching its identity (dedup like Add). selector, when non-empty,
// asserts which account the login was meant for. When the account is the
// currently active one, the live credential — presumably the dead one that
// prompted the re-login — is replaced as well.
func StoreLogin(selector, cred string, id *oauth.Identity) (*LoginResult, error) {
	if id == nil || id.Email == "" {
		return nil, errors.New("the token response did not include an account identity")
	}
	if err := paths.EnsureDirs(); err != nil {
		return nil, err
	}
	seq, err := account.Load()
	if err != nil {
		return nil, err
	}

	slot := seq.FindByIdentity(id.Email, id.OrganizationUUID)
	if selector != "" {
		want, rerr := seq.Resolve(selector)
		if rerr != nil {
			return nil, rerr
		}
		if slot != 0 && slot != want {
			return nil, fmt.Errorf("you logged in as %s, which is Account-%d, not Account-%d", id.Email, slot, want)
		}
		if slot == 0 {
			if existing := seq.Get(want); existing != nil && existing.Email != id.Email {
				return nil, fmt.Errorf("slot %d belongs to %s but you logged in as %s", want, existing.Email, id.Email)
			}
			slot = want
		}
	}
	res := &LoginResult{Email: id.Email}
	if slot == 0 {
		slot = seq.NextSlot()
		res.Created = true
	}
	res.Slot = slot

	if err := credentials.WriteBackup(slot, id.Email, cred); err != nil {
		return nil, fmt.Errorf("writing credential backup: %w", err)
	}
	cfg, err := loginConfigBackup(slot, id)
	if err != nil {
		return nil, err
	}
	if err := account.WriteFileAtomic(paths.AccountConfigBackup(slot, id.Email), cfg, 0o600); err != nil {
		return nil, fmt.Errorf("writing config backup: %w", err)
	}
	// A fresh credential invalidates any dead-token strike.
	_ = usage.Update(func(s *usage.Store) {
		if e := s.Get(slot, id.Email, id.OrganizationUUID); e != nil {
			e.ClearDeadToken()
		}
	})

	rec := seq.Get(slot)
	if rec == nil {
		rec = &account.Account{Added: time.Now().UTC().Format(account.TimeFormat)}
	}
	rec.Email = id.Email
	rec.UUID = id.UUID
	rec.OrganizationUUID = id.OrganizationUUID
	if id.OrganizationName != "" {
		rec.OrganizationName = id.OrganizationName
	}
	seq.Upsert(slot, rec)
	if err := seq.Save(); err != nil {
		return nil, err
	}

	if loginSlotIsActive(seq, slot) {
		if err := activateLogin(cred, cfg); err != nil {
			return res, fmt.Errorf("the login was saved, but replacing the live credential failed: %w", err)
		}
		res.Activated = true
	}
	return res, nil
}

// loginConfigBackup builds the slot's config backup for a re-login: the
// existing backup (or, for a new account, the live config) with oauthAccount
// replaced by the fresh identity.
func loginConfigBackup(slot int, id *oauth.Identity) ([]byte, error) {
	raw, err := os.ReadFile(paths.AccountConfigBackup(slot, id.Email))
	if errors.Is(err, os.ErrNotExist) {
		raw, err = claudecfg.ReadRaw()
		if errors.Is(err, os.ErrNotExist) {
			raw, err = []byte("{}"), nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("reading config for the backup: %w", err)
	}

	cfg := map[string]json.RawMessage{}
	if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
		return nil, fmt.Errorf("unparseable config for the backup: %w", jerr)
	}
	oa := map[string]any{
		"emailAddress":     id.Email,
		"accountUuid":      id.UUID,
		"organizationUuid": nil,
		"organizationName": nil,
	}
	if id.OrganizationUUID != "" {
		oa["organizationUuid"] = id.OrganizationUUID
	}
	if id.OrganizationName != "" {
		oa["organizationName"] = id.OrganizationName
	}
	oaRaw, err := json.Marshal(oa)
	if err != nil {
		return nil, err
	}
	cfg["oauthAccount"] = oaRaw
	return json.MarshalIndent(cfg, "", "  ")
}

// loginSlotIsActive reports whether slot is the live login: by the live
// config's identity when one is readable, by the roster pointer otherwise.
func loginSlotIsActive(seq *account.Sequence, slot int) bool {
	if liveID, err := claudecfg.ReadIdentity(); err == nil && liveID != nil {
		return seq.FindByIdentity(liveID.Email, liveID.OrganizationUUID) == slot
	}
	n, a := seq.Active()
	return a != nil && n == slot
}

// activateLogin replaces the live credential and config identity under the
// usual lock order. No outgoing backup is taken: the displaced credential
// belongs to the same account and is superseded by the fresh login.
func activateLogin(cred string, cfg []byte) error {
	swapLock := locks.NewFileLock(paths.LockPath())
	if err := swapLock.Acquire(); err != nil {
		return err
	}
	defer swapLock.Release()

	ccLocks := locks.ClaudeCredentialLocks(paths.ConfigHome())
	if err := locks.AcquireAll(ccLocks); err != nil {
		return err
	}
	defer locks.ReleaseAll(ccLocks)

	cfgLock := locks.ClaudeConfigLock(paths.GlobalConfigPath())
	if err := cfgLock.Acquire(); err != nil {
		return err
	}
	defer cfgLock.Release()

	live := credentials.ReadActive()
	if err := credentials.WriteActive(credentials.MergeShared(cred, live.Value)); err != nil {
		return err
	}
	return claudecfg.SpliceOAuthAccount(cfg)
}
