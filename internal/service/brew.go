// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"path/filepath"
	"strings"
)

// Homebrew's `brew services` names a formula's service homebrew.mxcl.<formula>
// under launchd and homebrew.<formula> under systemd. The cswap formula
// declares `service do run [opt_bin/"cswap", "auto"]`, so a brew-managed
// service runs the very loop cswap's own service would.
const (
	brewLaunchdLabel = "homebrew.mxcl.cswap"
	brewSystemdUnit  = "homebrew.cswap.service"
)

// launchDaemonsDir is where `sudo brew services` puts a root-level plist.
// A variable so tests can point it into a temp dir.
var launchDaemonsDir = "/Library/LaunchDaemons"

// brewService describes a Homebrew-managed cswap service, if there is one.
type brewService struct {
	Name  string // launchd label or systemd unit
	State string // human summary: loaded/running/pid, or the unit's state
	Where string // where the definition was found
	found bool
}

// Present reports whether a brew-managed service is set up.
func (b brewService) Present() bool { return b.found }

// detectBrew looks for a Homebrew-managed cswap service. It is read-only:
// a stat of the definition plus one launchctl print / systemctl is-active.
func detectBrew() brewService {
	if goos == "darwin" {
		return detectBrewLaunchd()
	}
	if goos == "linux" {
		return detectBrewSystemd()
	}
	return brewService{}
}

func detectBrewLaunchd() brewService {
	b := brewService{Name: brewLaunchdLabel}
	home, _ := os.UserHomeDir()
	candidates := []struct{ path, domain string }{
		{filepath.Join(home, "Library", "LaunchAgents", brewLaunchdLabel+".plist"), domainTarget()},
		{filepath.Join(launchDaemonsDir, brewLaunchdLabel+".plist"), "system"},
	}
	for _, c := range candidates {
		_, statErr := os.Stat(c.path)
		out, err := run("launchctl", "print", c.domain+"/"+brewLaunchdLabel)
		switch {
		case err == nil:
			// Loaded counts even without a plist on disk: `brew services run`
			// loads the keg's plist without installing it.
			b.found = true
			b.State = launchdSummary(out)
			b.Where = "plist: " + c.path
			if statErr != nil {
				b.Where = "loaded by launchd in " + c.domain + " (no plist at " + c.path + ")"
			}
			return b
		case statErr == nil:
			b.found = true
			b.State = "installed but not loaded"
			b.Where = "plist: " + c.path
			return b
		}
	}
	return b
}

func detectBrewSystemd() brewService {
	b := brewService{Name: brewSystemdUnit}
	path := filepath.Join(filepath.Dir(unitPath()), brewSystemdUnit)
	out, _ := run("systemctl", "--user", "is-active", brewSystemdUnit)
	state := strings.TrimSpace(string(out))
	if _, err := os.Stat(path); err == nil {
		b.found = true
		b.Where = "unit: " + path
	} else if state == "active" || state == "activating" || state == "reloading" {
		b.found = true
		b.Where = "active in systemd --user (no unit at " + path + ")"
	}
	if state == "" {
		state = "unknown"
	}
	b.State = state + " (journalctl --user -u " + brewSystemdUnit + " for logs)"
	return b
}

// launchdSummary condenses `launchctl print` output to its state, pid and
// log path. Only the first occurrence of each key counts: nested sections
// (endpoints, sockets) repeat keys like "state =" with other meanings.
func launchdSummary(out []byte) string {
	parts := []string{"loaded"}
	var log string
	seen := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		l := strings.TrimSpace(line)
		for _, key := range []string{"state =", "pid ="} {
			if strings.HasPrefix(l, key) && !seen[key] {
				seen[key] = true
				parts = append(parts, l)
			}
		}
		if v, ok := strings.CutPrefix(l, "stdout path = "); ok && log == "" {
			log = v
		}
	}
	s := strings.Join(parts, ", ")
	if log != "" {
		s += " (log: " + log + ")"
	}
	return s
}
