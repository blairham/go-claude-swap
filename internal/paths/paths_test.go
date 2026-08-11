package paths

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfigHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/claude")
	if got := ConfigHome(); got != "/custom/claude" {
		t.Errorf("ConfigHome = %q", got)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got := ConfigHome(); !strings.HasSuffix(got, "/.claude") {
		t.Errorf("default ConfigHome = %q", got)
	}
}

func TestGlobalConfigDefaultIsInHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	// The default .claude.json lives in $HOME, not inside ~/.claude.
	if got := GlobalConfigPath(); got != filepath.Join(home, ".claude.json") {
		t.Errorf("GlobalConfigPath = %q", got)
	}
}

func TestBackupRootPerPlatform(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	switch runtime.GOOS {
	case "linux":
		t.Setenv("XDG_DATA_HOME", "")
		want := filepath.Join(home, ".local", "share", "claude-swap")
		if got := BackupRoot(); got != want {
			t.Errorf("BackupRoot = %q, want %q", got, want)
		}
		t.Setenv("XDG_DATA_HOME", "/abs/data")
		if got := BackupRoot(); got != "/abs/data/claude-swap" {
			t.Errorf("BackupRoot with XDG = %q", got)
		}
		// Non-absolute XDG values are ignored.
		t.Setenv("XDG_DATA_HOME", "relative/data")
		if got := BackupRoot(); got != want {
			t.Errorf("BackupRoot with relative XDG = %q, want %q", got, want)
		}
	default:
		want := filepath.Join(home, ".claude-swap-backup")
		if got := BackupRoot(); got != want {
			t.Errorf("BackupRoot = %q, want %q", got, want)
		}
	}
}

func TestBackupFileNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := filepath.Base(AccountCredsBackup(2, "a@b.c")); got != ".creds-2-a@b.c.enc" {
		t.Errorf("creds backup name = %q", got)
	}
	if got := filepath.Base(AccountConfigBackup(2, "a@b.c")); got != ".claude-config-2-a@b.c.json" {
		t.Errorf("config backup name = %q", got)
	}
}
