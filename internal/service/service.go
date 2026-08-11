// Package service installs the auto-switch loop as a login service that
// runs continuously and restarts on crash and reboot: a launchd
// LaunchAgent on macOS, a systemd user unit on Linux.
//
// A user-level service (not a root daemon) is deliberate: on macOS the
// Keychain is only unlockable inside the user's login session, and on both
// platforms the service must see the user's $HOME.
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// Label identifies the service to launchd/systemd.
const Label = "com.blairham.cswap-auto"

// LogPath is where the service's stdout/stderr goes.
func LogPath() string { return filepath.Join(paths.BackupRoot(), "cswap-auto.log") }

// Supported reports whether service management works on this platform.
func Supported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

// executable resolves the running binary's absolute path; the service must
// point at a stable location, not a relative invocation.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Install writes the service definition and starts it. extraArgs are
// appended to `cswap auto` (e.g. --json).
func Install(extraArgs []string) (string, error) {
	if !Supported() {
		return "", fmt.Errorf("service install is not supported on %s", runtime.GOOS)
	}
	exe, err := executable()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the cswap binary path: %w", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return installLaunchd(exe, extraArgs)
	}
	return installSystemd(exe, extraArgs)
}

// Uninstall stops the service and removes its definition.
func Uninstall() (string, error) {
	if !Supported() {
		return "", fmt.Errorf("service uninstall is not supported on %s", runtime.GOOS)
	}
	if runtime.GOOS == "darwin" {
		return uninstallLaunchd()
	}
	return uninstallSystemd()
}

// Status returns a human-readable service status.
func Status() (string, error) {
	if !Supported() {
		return "", fmt.Errorf("service status is not supported on %s", runtime.GOOS)
	}
	if runtime.GOOS == "darwin" {
		return statusLaunchd()
	}
	return statusSystemd()
}
