// Package keychain wraps the macOS `security` CLI for generic-password
// items. The binary path is pinned to /usr/bin/security (never PATH) so the
// Keychain ACL entry survives interpreter changes, and every spawn has a 5s
// timeout. On other platforms every call reports ErrUnavailable.
package keychain

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"
)

// Service names used by cswap and Claude Code.
const (
	// ServiceClaudeCodeOAuth holds Claude Code's active OAuth credential.
	ServiceClaudeCodeOAuth = "Claude Code-credentials"
	// ServiceClaudeCodeAPIKey holds Claude Code's active managed API key.
	ServiceClaudeCodeAPIKey = "Claude Code"
	// ServiceSwap holds cswap's per-account backups.
	ServiceSwap = "claude-swap"

	securityBin = "/usr/bin/security"
	timeout     = 5 * time.Second

	// rc 44 = errSecItemNotFound
	rcNotFound = 44
)

// ErrUnavailable means the Keychain backend cannot be used on this platform
// or the security tool failed; callers fall back to file storage.
var ErrUnavailable = errors.New("keychain unavailable")

// ErrNotFound means the item does not exist (distinct from a failed read).
var ErrNotFound = errors.New("keychain item not found")

// Available reports whether the Keychain backend exists on this platform.
// CSWAP_DISABLE_KEYCHAIN=1 forces the file backend (used by tests and for
// debugging).
func Available() bool {
	if runtime.GOOS != "darwin" || os.Getenv("CSWAP_DISABLE_KEYCHAIN") == "1" {
		return false
	}
	_, err := os.Stat(securityBin)
	return err == nil
}

// UserAccountName matches Claude Code's getUsername(): $USER, then the
// process owner, then a fixed fallback.
func UserAccountName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "claude-code-user"
}

// BackupAccountName names cswap's backup item for a slot/email.
func BackupAccountName(slot int, email string) string {
	return fmt.Sprintf("account-%d-%s", slot, email)
}

func run(args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, securityBin, args...)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", -1, fmt.Errorf("%w: security timed out", ErrUnavailable)
	}
	if err != nil {
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			return string(out), ee.ExitCode(), nil
		}
		return "", -1, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return string(out), 0, nil
}

// Get reads an item's value. Exactly one trailing newline is stripped,
// preserving any other whitespace in the stored value.
func Get(service, accountName string) (string, error) {
	if !Available() {
		return "", ErrUnavailable
	}
	out, rc, err := run("find-generic-password", "-a", accountName, "-w", "-s", service)
	if err != nil {
		return "", err
	}
	switch rc {
	case 0:
		return strings.TrimSuffix(out, "\n"), nil
	case rcNotFound:
		return "", ErrNotFound
	default:
		return "", fmt.Errorf("%w: security exited %d", ErrUnavailable, rc)
	}
}

// Set writes an item, replacing any existing value (-U). The value is passed
// hex-encoded (-X) so the secret never appears verbatim; when the command
// line fits, it is piped through `security -i` stdin to keep it out of argv.
func Set(service, accountName, value string) error {
	if !Available() {
		return ErrUnavailable
	}
	hexVal := hex.EncodeToString([]byte(value))

	// Preferred transport: whole command through stdin (`security -i`).
	// The stdin parser reads lines with a 4096-byte fgets buffer.
	line := fmt.Sprintf("add-generic-password -U -a %s -s %s -X %s",
		quoteSecurityArg(accountName), quoteSecurityArg(service), hexVal)
	if len(line) <= 4096-64 {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, securityBin, "-i")
		cmd.Stdin = strings.NewReader(line + "\n")
		if err := cmd.Run(); err != nil || ctx.Err() != nil {
			return fmt.Errorf("%w: add-generic-password failed", ErrUnavailable)
		}
		return nil
	}

	_, rc, err := run("add-generic-password", "-U", "-a", accountName, "-s", service, "-X", hexVal)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("%w: add-generic-password exited %d", ErrUnavailable, rc)
	}
	return nil
}

// Delete removes an item; a missing item is success.
func Delete(service, accountName string) error {
	if !Available() {
		return ErrUnavailable
	}
	_, rc, err := run("delete-generic-password", "-a", accountName, "-s", service)
	if err != nil {
		return err
	}
	if rc != 0 && rc != rcNotFound {
		return fmt.Errorf("%w: delete-generic-password exited %d", ErrUnavailable, rc)
	}
	return nil
}

// quoteSecurityArg double-quotes an argument for the `security -i` line
// parser, escaping backslash and double-quote.
func quoteSecurityArg(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
