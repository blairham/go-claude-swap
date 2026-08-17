// Package paths resolves Claude Code's config locations and cswap's backup
// root, mirroring claude-swap's per-OS layout exactly so both tools can
// coexist on one machine.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Platform identifies the storage backend family.
type Platform int

// Platform values.
const (
	MacOS Platform = iota
	Linux
	WSL
	Windows
	Unknown
)

// DetectPlatform classifies the current OS; WSL is Linux with
// WSL_DISTRO_NAME set.
func DetectPlatform() Platform {
	switch runtime.GOOS {
	case "darwin":
		return MacOS
	case "windows":
		return Windows
	case "linux":
		if os.Getenv("WSL_DISTRO_NAME") != "" {
			return WSL
		}
		return Linux
	default:
		return Unknown
	}
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// ConfigHome is Claude Code's config directory: $CLAUDE_CONFIG_DIR or ~/.claude.
func ConfigHome() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".claude")
}

// GlobalConfigPath is Claude Code's global config: <config_home>/.config.json
// if it exists (legacy), else ($CLAUDE_CONFIG_DIR || $HOME)/.claude.json.
// Note the asymmetry: the default .claude.json lives in $HOME, not in ~/.claude.
func GlobalConfigPath() string {
	legacy := filepath.Join(ConfigHome(), ".config.json")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = home()
	}
	return filepath.Join(base, ".claude.json")
}

// CredentialsFilePath is the plaintext credential file used on non-macOS
// platforms (and as a macOS fallback).
func CredentialsFilePath() string {
	return filepath.Join(ConfigHome(), ".credentials.json")
}

// ClaudeSettingsPath is Claude Code's user-level settings file, where the
// selected model is recorded: <config_home>/settings.json.
func ClaudeSettingsPath() string {
	return filepath.Join(ConfigHome(), "settings.json")
}

// BackupRoot is cswap's data directory. Linux/WSL follow XDG; macOS and
// Windows use the legacy ~/.claude-swap-backup layout.
func BackupRoot() string {
	switch DetectPlatform() {
	case Linux, WSL:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			if rest, ok := strings.CutPrefix(xdg, "~"); ok {
				xdg = filepath.Join(home(), rest)
			}
			if filepath.IsAbs(xdg) {
				return filepath.Join(xdg, "claude-swap")
			}
		}
		return filepath.Join(home(), ".local", "share", "claude-swap")
	default:
		return filepath.Join(home(), ".claude-swap-backup")
	}
}

// Derived locations under the backup root.

// SequencePath is the account roster file.
func SequencePath() string { return filepath.Join(BackupRoot(), "sequence.json") }

// ConfigsDir holds per-account config backups.
func ConfigsDir() string { return filepath.Join(BackupRoot(), "configs") }

// CredentialsDir holds per-account credential backups.
func CredentialsDir() string { return filepath.Join(BackupRoot(), "credentials") }

// SettingsPath is cswap's own settings file.
func SettingsPath() string { return filepath.Join(BackupRoot(), "settings.json") }

// CacheDir holds the usage store.
func CacheDir() string { return filepath.Join(BackupRoot(), "cache") }

// UsageStorePath is the cached usage database.
func UsageStorePath() string { return filepath.Join(CacheDir(), "usage.json") }

// LockPath is cswap's own switch lock.
func LockPath() string { return filepath.Join(BackupRoot(), ".lock") }

// SocketPath is the unix socket a running `cswap auto` loop serves its
// control API on.
func SocketPath() string { return filepath.Join(BackupRoot(), "cswap.sock") }

// AccountConfigBackup names the config backup for a slot/email.
func AccountConfigBackup(slot int, email string) string {
	return filepath.Join(ConfigsDir(), ".claude-config-"+strconv.Itoa(slot)+"-"+email+".json")
}

// AccountCredsBackup names the credential backup (.enc) for a slot/email.
func AccountCredsBackup(slot int, email string) string {
	return filepath.Join(CredentialsDir(), ".creds-"+strconv.Itoa(slot)+"-"+email+".enc")
}

// EnsureDirs creates the backup layout with 0700 permissions.
func EnsureDirs() error {
	for _, d := range []string{BackupRoot(), ConfigsDir(), CredentialsDir(), CacheDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
