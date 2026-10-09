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
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// Label identifies the service to launchd/systemd.
const Label = "com.blairham.cswap-auto"

// LogPath is where the service's stdout/stderr goes.
func LogPath() string { return filepath.Join(paths.BackupRoot(), "cswap-auto.log") }

// goos selects the service backend. A variable rather than runtime.GOOS
// so tests can exercise both backends on either CI runner.
var goos = runtime.GOOS

// run executes an external service-manager command (launchctl, systemctl)
// and returns its combined output. Swapped out in tests so they never touch
// the real launchd or systemd.
var run = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// Supported reports whether service management works on this platform.
func Supported() bool {
	return goos == "darwin" || goos == "linux"
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
		return "", fmt.Errorf("service install is not supported on %s", goos)
	}
	// A second loop would compete with the brew-managed one for the usage
	// request budget and the control socket, so refuse rather than stack.
	if b := detectBrew(); b.Present() {
		return "", fmt.Errorf("the Homebrew-managed service %s is already set up (%s); "+
			"it runs the same `cswap auto` loop, so installing %s too would run two. "+
			"Manage it with `brew services`, or run `brew services stop cswap` first if you want cswap's own service instead",
			b.Name, b.Where, ownName())
	}
	exe, err := executable()
	if err != nil {
		return "", fmt.Errorf("cannot resolve the cswap binary path: %w", err)
	}
	if err := paths.EnsureDirs(); err != nil {
		return "", err
	}
	if goos == "darwin" {
		return installLaunchd(exe, extraArgs)
	}
	return installSystemd(exe, extraArgs)
}

// Uninstall stops the service and removes its definition. It only ever
// touches cswap's own service; a Homebrew-managed one belongs to
// `brew services`, so it is pointed at rather than removed.
func Uninstall() (string, error) {
	if !Supported() {
		return "", fmt.Errorf("service uninstall is not supported on %s", goos)
	}
	b := detectBrew()
	if !ownInstalled() && b.Present() {
		return "", fmt.Errorf("%s is not installed; the running loop is the Homebrew-managed service %s (%s). "+
			"Stop it with `brew services stop cswap`", ownName(), b.Name, b.Where)
	}
	var msg string
	var err error
	if goos == "darwin" {
		msg, err = uninstallLaunchd()
	} else {
		msg, err = uninstallSystemd()
	}
	if err == nil && b.Present() {
		msg += "\nThe Homebrew-managed service " + b.Name + " is still set up; stop it with `brew services stop cswap`."
	}
	return msg, err
}

// ownName names cswap's own service for the current backend.
func ownName() string {
	if goos == "darwin" {
		return "LaunchAgent " + Label
	}
	return "systemd user unit " + unitName
}

// ownInstalled reports whether cswap's own service definition exists.
func ownInstalled() bool {
	p := unitPath()
	if goos == "darwin" {
		p = plistPath()
	}
	_, err := os.Stat(p)
	return err == nil
}

// Status returns a human-readable service status covering both cswap's own
// service and a Homebrew-managed one, since either can be what runs the loop.
func Status() (string, error) {
	if !Supported() {
		return "", fmt.Errorf("service status is not supported on %s", goos)
	}
	var own string
	var err error
	if goos == "darwin" {
		own, err = statusLaunchd()
	} else {
		own, err = statusSystemd()
	}
	if err != nil {
		return "", err
	}
	out := ownName() + ": " + own
	if b := detectBrew(); b.Present() {
		out += "\nHomebrew service " + b.Name + ": " + b.State + "\n  " + b.Where +
			"\n  managed by `brew services` (stop with `brew services stop cswap`)"
	} else {
		out += "\nHomebrew service: not installed"
	}
	return out, nil
}
