// Package claudecfg reads and edits Claude Code's global config
// (~/.claude.json). Only the oauthAccount object is account-specific; every
// other key is machine state and must be preserved across switches.
package claudecfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// Identity is the logged-in account per the global config.
type Identity struct {
	Email            string
	AccountUUID      string
	OrganizationUUID string
	OrganizationName string
}

// ReadIdentity extracts oauthAccount from the global config. Returns
// (nil, nil) when no account is logged in; an unreadable file is an error.
func ReadIdentity() (*Identity, error) {
	raw, err := os.ReadFile(paths.GlobalConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		OAuthAccount *struct {
			EmailAddress     string  `json:"emailAddress"`
			AccountUUID      string  `json:"accountUuid"`
			OrganizationUUID *string `json:"organizationUuid"`
			OrganizationName *string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
		return nil, fmt.Errorf("unparseable %s: %w", paths.GlobalConfigPath(), jerr)
	}
	if cfg.OAuthAccount == nil || cfg.OAuthAccount.EmailAddress == "" {
		return nil, nil
	}
	id := &Identity{
		Email:       cfg.OAuthAccount.EmailAddress,
		AccountUUID: cfg.OAuthAccount.AccountUUID,
	}
	if cfg.OAuthAccount.OrganizationUUID != nil {
		id.OrganizationUUID = *cfg.OAuthAccount.OrganizationUUID
	}
	if cfg.OAuthAccount.OrganizationName != nil {
		id.OrganizationName = *cfg.OAuthAccount.OrganizationName
	}
	return id, nil
}

// ReadRaw returns the global config text verbatim.
func ReadRaw() ([]byte, error) {
	return os.ReadFile(paths.GlobalConfigPath())
}

// SpliceOAuthAccount writes backupConfig's oauthAccount into the live
// config, preserving everything else. Only when the live config is absent
// or unparseable (salvage copy made first) is the backup written whole.
func SpliceOAuthAccount(backupConfig []byte) error {
	var backup map[string]json.RawMessage
	if err := json.Unmarshal(backupConfig, &backup); err != nil {
		return fmt.Errorf("backup config unparseable: %w", err)
	}
	oa, ok := backup["oauthAccount"]
	if !ok {
		return errors.New("backup config has no oauthAccount")
	}

	path := paths.GlobalConfigPath()
	raw, err := os.ReadFile(path)
	if err == nil {
		var live map[string]json.RawMessage
		if json.Unmarshal(raw, &live) == nil {
			live["oauthAccount"] = oa
			data, merr := json.MarshalIndent(live, "", "  ")
			if merr != nil {
				return merr
			}
			return account.WriteFileAtomic(path, data, 0o600)
		}
		// Present but unparseable: keep a salvage copy, then replace whole.
		salvage(path, raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return account.WriteFileAtomic(path, backupConfig, 0o600)
}

// RestoreRaw writes exact config text back (rollback path).
func RestoreRaw(text []byte) error {
	return account.WriteFileAtomic(paths.GlobalConfigPath(), text, 0o600)
}

func salvage(path string, raw []byte) {
	base := path + ".unreadable-" + strconv.FormatInt(time.Now().Unix(), 10)
	target := base
	for i := 1; ; i++ {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			break
		}
		target = base + "." + strconv.Itoa(i)
	}
	_ = account.WriteFileAtomic(target, raw, 0o600)
}
