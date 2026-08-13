// Package credentials manages the active Claude Code credential (Keychain
// on macOS, plaintext file elsewhere) and cswap's per-account backups
// (Keychain-primary on macOS with base64 .enc file fallback).
//
// Two distinctions run through every function here:
//   - absent vs unreadable: an unreadable store must never be treated as
//     empty (it gates re-add advice and backup overwrites);
//   - OAuth vs managed API key: the two auth axes are mutually exclusive in
//     the live store, and writing one clears the other.
package credentials

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/keychain"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// SharedKeys are machine-shared credential fields (MCP logins, plugin
// secrets): on activation the live credential's copies win over the target
// slot's stored copies, so switching accounts never logs MCP servers out.
var SharedKeys = map[string]bool{
	"mcpOAuth":             true,
	"mcpOAuthClientConfig": true,
	"mcpXaaIdp":            true,
	"mcpXaaIdpConfig":      true,
	"pluginSecrets":        true,
}

// IsAPIKey reports whether a credential string is a managed API key rather
// than an OAuth JSON blob.
func IsAPIKey(cred string) bool {
	t := strings.TrimSpace(cred)
	return strings.HasPrefix(t, "sk-ant-api") && !strings.HasPrefix(t, "{")
}

// Active is the result of reading the live credential.
type Active struct {
	// Value is the credential string; "" means nothing found anywhere.
	Value string
	// Unreadable is true when a store errored (≠ absent).
	Unreadable bool
	// Degraded is true on macOS when the OAuth Keychain read failed but a
	// plaintext fallback answered; such bytes may be served but never
	// captured or refreshed (they may be a consumed predecessor).
	Degraded bool
}

// useKeychain is a per-process sticky capability cache: a failed Keychain
// write pins file mode for the rest of the process.
var keychainPinned bool

func keychainUsable() bool {
	return keychain.Available() && !keychainPinned
}

// PinFileMode forces file-mode storage for the rest of the process.
func PinFileMode() { keychainPinned = true }

// ReadActive reads the live credential: macOS Keychain first, then the
// plaintext .credentials.json, then the managed API key stores.
func ReadActive() Active {
	var res Active
	keychainFailed := false

	if keychainUsable() {
		v, err := readKeychainRetry(keychain.ServiceClaudeCodeOAuth)
		switch {
		case err == nil:
			return Active{Value: v}
		case errors.Is(err, keychain.ErrNotFound):
			// absent, keep looking
		default:
			keychainFailed = true
		}
	}

	raw, err := os.ReadFile(paths.CredentialsFilePath())
	if err == nil && strings.TrimSpace(string(raw)) != "" {
		res.Value = string(raw)
		res.Degraded = keychainFailed
		return res
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Active{Unreadable: true, Degraded: keychainFailed}
	}

	// Managed API key axis.
	if keychainUsable() {
		if v, kerr := keychain.Get(keychain.ServiceClaudeCodeAPIKey, keychain.UserAccountName()); kerr == nil && v != "" {
			return Active{Value: v, Degraded: keychainFailed}
		}
	}
	if key := readPrimaryAPIKey(); key != "" {
		return Active{Value: key, Degraded: keychainFailed}
	}

	return Active{Degraded: keychainFailed, Unreadable: false}
}

func readKeychainRetry(service string) (string, error) {
	acct := keychain.UserAccountName()
	v, err := keychain.Get(service, acct)
	if err != nil && !errors.Is(err, keychain.ErrNotFound) {
		// One retry on genuine failure (not on absence).
		v, err = keychain.Get(service, acct)
	}
	return v, err
}

func readPrimaryAPIKey() string {
	raw, err := os.ReadFile(paths.GlobalConfigPath())
	if err != nil {
		return ""
	}
	var cfg struct {
		PrimaryAPIKey string `json:"primaryApiKey"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	return cfg.PrimaryAPIKey
}

// WriteActive writes a credential into the live store, enforcing the
// single-auth-axis invariant.
func WriteActive(cred string) error {
	if IsAPIKey(cred) {
		return writeActiveAPIKey(cred)
	}
	return writeActiveOAuth(cred)
}

func writeActiveOAuth(cred string) error {
	credFile := paths.CredentialsFilePath()
	if keychainUsable() {
		err := keychain.Set(keychain.ServiceClaudeCodeOAuth, keychain.UserAccountName(), cred)
		if err == nil {
			// Rewrite an already-present plaintext file with the same bytes:
			// the mtime bump makes a running Claude Code hot-reload. Never
			// create it, never delete it.
			if _, serr := os.Stat(credFile); serr == nil {
				_ = account.WriteFileAtomic(credFile, []byte(cred), 0o600)
			}
			clearManagedKey()
			return nil
		}
		// Keychain write failed: fall to file mode and stay there. The
		// shadowing Keychain item is deleted below, after the file write
		// succeeds — never before the replacement bytes exist somewhere.
		PinFileMode()
	}
	if err := account.WriteFileAtomic(credFile, []byte(cred), 0o600); err != nil {
		return err
	}
	// File mode: an uncleared live Keychain item (from a previous account)
	// would shadow the file — Claude Code reads the Keychain first — so
	// delete it once the file holds the new bytes. This also covers file
	// mode pinned by an earlier failure elsewhere in the process.
	if keychain.Available() {
		_ = keychain.Delete(keychain.ServiceClaudeCodeOAuth, keychain.UserAccountName())
	}
	clearManagedKey()
	return nil
}

func writeActiveAPIKey(key string) error {
	key = strings.TrimSpace(key)
	if err := approveAPIKey(key); err != nil {
		return err
	}
	stored := false
	if keychainUsable() {
		if keychain.Set(keychain.ServiceClaudeCodeAPIKey, keychain.UserAccountName(), key) == nil {
			stored = true
		}
	}
	if !stored {
		if err := updateGlobalConfigKey("primaryApiKey", key); err != nil {
			return err
		}
	}
	// Clear the OAuth axis.
	if keychain.Available() {
		_ = keychain.Delete(keychain.ServiceClaudeCodeOAuth, keychain.UserAccountName())
	}
	os.Remove(paths.CredentialsFilePath())
	return nil
}

// approveAPIKey appends the key's last 20 characters to
// customApiKeyResponses.approved so Claude Code accepts it without prompting.
func approveAPIKey(key string) error {
	tail := key
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	return mutateGlobalConfig(func(cfg map[string]any) {
		resp, _ := cfg["customApiKeyResponses"].(map[string]any)
		if resp == nil {
			resp = map[string]any{"approved": []any{}, "rejected": []any{}}
		}
		approved, _ := resp["approved"].([]any)
		for _, a := range approved {
			if s, ok := a.(string); ok && s == tail {
				cfg["customApiKeyResponses"] = resp
				return
			}
		}
		resp["approved"] = append(approved, tail)
		cfg["customApiKeyResponses"] = resp
	})
}

func clearManagedKey() {
	if keychain.Available() {
		_ = keychain.Delete(keychain.ServiceClaudeCodeAPIKey, keychain.UserAccountName())
	}
	_ = mutateGlobalConfig(func(cfg map[string]any) {
		delete(cfg, "primaryApiKey")
	})
}

func updateGlobalConfigKey(key string, value any) error {
	return mutateGlobalConfig(func(cfg map[string]any) {
		cfg[key] = value
	})
}

// mutateGlobalConfig does a key-scoped read-modify-write of ~/.claude.json,
// preserving all other keys. It refuses to write when the file exists but
// cannot be parsed (never clobber a torn config with {}).
func mutateGlobalConfig(mutate func(map[string]any)) error {
	path := paths.GlobalConfigPath()
	cfg := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
			return fmt.Errorf("refusing to modify unparseable %s: %w", path, jerr)
		}
	case errors.Is(err, os.ErrNotExist):
		// fresh file
	default:
		return fmt.Errorf("refusing to modify unreadable %s: %w", path, err)
	}
	mutate(cfg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return account.WriteFileAtomic(path, data, 0o600)
}

// MergeShared splices the live credential's machine-shared fields into the
// target slot's credential before activation. Account-scoped keys
// (claudeAiOauth, trustedDeviceToken, anything unrecognized) stay with the
// slot. No-op unless both sides are OAuth JSON objects.
func MergeShared(target, live string) string {
	var targetObj, liveObj map[string]json.RawMessage
	if json.Unmarshal([]byte(target), &targetObj) != nil || targetObj["claudeAiOauth"] == nil {
		return target
	}
	if json.Unmarshal([]byte(live), &liveObj) != nil || liveObj["claudeAiOauth"] == nil {
		return target
	}
	for k := range SharedKeys {
		delete(targetObj, k)
		if v, ok := liveObj[k]; ok {
			targetObj[k] = v
		}
	}
	out, err := json.Marshal(targetObj)
	if err != nil {
		return target
	}
	return string(out)
}

// --- Per-account backups -------------------------------------------------

// ReadBackup reads a slot's credential backup. On macOS a valid non-empty
// .enc file wins over the Keychain (it may hold bytes written while the
// Keychain was down); corrupt or absent .enc falls through to the Keychain.
// Returns (value, unreadable, err); absent everywhere is ("", false, nil).
func ReadBackup(slot int, email string) (string, bool) {
	return readBackupStore(paths.AccountCredsBackup(slot, email), keychain.BackupAccountName(slot, email))
}

// ReadPrevBackup reads the previous generation of a slot's credential backup
// (retained by WriteBackup). Same precedence rules as ReadBackup.
func ReadPrevBackup(slot int, email string) (string, bool) {
	return readBackupStore(paths.AccountCredsBackup(slot, email)+".prev", keychain.BackupAccountName(slot, email)+".prev")
}

func readBackupStore(encPath, keychainAccount string) (string, bool) {
	raw, err := os.ReadFile(encPath)
	if err == nil {
		decoded, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if derr == nil && len(decoded) > 0 {
			return string(decoded), false
		}
		// corrupt .enc: fall through to keychain
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", true
	}

	if keychainUsable() {
		v, kerr := keychain.Get(keychain.ServiceSwap, keychainAccount)
		switch {
		case kerr == nil:
			return v, false
		case errors.Is(kerr, keychain.ErrNotFound):
			return "", false
		default:
			return "", true
		}
	}
	return "", false
}

// WriteBackup stores a slot's credential backup, retaining the previous
// generation as .prev first. Keychain-primary on macOS: after a successful
// Keychain write the .enc is deleted so it cannot shadow future reads.
func WriteBackup(slot int, email, cred string) error {
	retainPrev(slot, email)

	encPath := paths.AccountCredsBackup(slot, email)
	if keychainUsable() {
		if keychain.Set(keychain.ServiceSwap, keychain.BackupAccountName(slot, email), cred) == nil {
			if err := os.Remove(encPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				// Couldn't delete the stale .enc: rewrite it with fresh
				// bytes so .enc-wins reads stay correct.
				return account.WriteFileAtomic(encPath, []byte(base64.StdEncoding.EncodeToString([]byte(cred))), 0o600)
			}
			return nil
		}
		PinFileMode()
	}
	if err := account.WriteFileAtomic(encPath, []byte(base64.StdEncoding.EncodeToString([]byte(cred))), 0o600); err != nil {
		return err
	}
	if keychain.Available() {
		_ = keychain.Delete(keychain.ServiceSwap, keychain.BackupAccountName(slot, email))
	}
	return nil
}

func retainPrev(slot int, email string) {
	cur, unreadable := ReadBackup(slot, email)
	if unreadable || cur == "" {
		return
	}
	if keychainUsable() {
		if keychain.Set(keychain.ServiceSwap, keychain.BackupAccountName(slot, email)+".prev", cur) == nil {
			return
		}
	}
	prevPath := paths.AccountCredsBackup(slot, email) + ".prev"
	_ = account.WriteFileAtomic(prevPath, []byte(base64.StdEncoding.EncodeToString([]byte(cur))), 0o600)
}

// DeleteBackup removes a slot's backups: .enc, .enc.prev, Keychain items
// (including the legacy account-None-{email} alias).
func DeleteBackup(slot int, email string) {
	os.Remove(paths.AccountCredsBackup(slot, email))
	os.Remove(paths.AccountCredsBackup(slot, email) + ".prev")
	if keychain.Available() {
		_ = keychain.Delete(keychain.ServiceSwap, keychain.BackupAccountName(slot, email))
		_ = keychain.Delete(keychain.ServiceSwap, keychain.BackupAccountName(slot, email)+".prev")
		_ = keychain.Delete(keychain.ServiceSwap, "account-None-"+email)
	}
}
