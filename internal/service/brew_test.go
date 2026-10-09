package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHost redirects every filesystem path and external command the service
// package uses, so tests never see the real launchd, systemd or $HOME.
type fakeHost struct {
	home  string
	calls []string
	// replies maps "name arg arg..." to its output; anything else fails.
	replies map[string]string
}

func newFakeHost(t *testing.T, platform string) *fakeHost {
	t.Helper()
	h := &fakeHost{home: t.TempDir(), replies: map[string]string{}}
	t.Setenv("HOME", h.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h.home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h.home, ".local", "share"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h.home, ".claude"))
	oldGOOS, oldRun, oldDaemons := goos, run, launchDaemonsDir
	t.Cleanup(func() { goos, run, launchDaemonsDir = oldGOOS, oldRun, oldDaemons })
	goos = platform
	launchDaemonsDir = filepath.Join(h.home, "LaunchDaemons")
	run = func(name string, args ...string) ([]byte, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		h.calls = append(h.calls, key)
		if out, ok := h.replies[key]; ok {
			return []byte(out), nil
		}
		return []byte("Could not find service"), errors.New("exit status 113")
	}
	return h
}

func (h *fakeHost) writeFile(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(h.home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (h *fakeHost) ran(prefix string) bool {
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// A trimmed `launchctl print gui/<uid>/homebrew.mxcl.cswap` from a machine
// running the brew service; the nested "state =" must not be picked up.
const brewLaunchctlPrint = `gui/501/homebrew.mxcl.cswap = {
	active count = 1
	path = /Users/u/Library/LaunchAgents/homebrew.mxcl.cswap.plist
	type = LaunchAgent
	state = running

	program = /opt/homebrew/opt/cswap/bin/cswap
	stdout path = /opt/homebrew/var/log/cswap-auto.log
	stderr path = /opt/homebrew/var/log/cswap-auto.log
	pid = 2809
	endpoints = {
		state = inactive
	}
}
`

func brewPrintKey() string { return "launchctl print " + domainTarget() + "/" + brewLaunchdLabel }

func TestStatusReportsRunningBrewLaunchAgent(t *testing.T) {
	h := newFakeHost(t, "darwin")
	plist := h.writeFile(t, "Library/LaunchAgents/homebrew.mxcl.cswap.plist")
	h.replies[brewPrintKey()] = brewLaunchctlPrint

	got, err := Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LaunchAgent " + Label + ": not installed",
		"Homebrew service homebrew.mxcl.cswap: loaded, state = running, pid = 2809 (log: /opt/homebrew/var/log/cswap-auto.log)",
		"plist: " + plist,
		"brew services stop cswap",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "inactive") {
		t.Errorf("nested state leaked into the summary:\n%s", got)
	}
}

func TestStatusDetectsBrewLoadedWithoutPlist(t *testing.T) {
	// `brew services run` loads the keg's plist without installing one.
	h := newFakeHost(t, "darwin")
	h.replies[brewPrintKey()] = brewLaunchctlPrint

	got, err := Status()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Homebrew service homebrew.mxcl.cswap: loaded, state = running") ||
		!strings.Contains(got, "no plist at") {
		t.Errorf("unexpected status:\n%s", got)
	}
}

func TestStatusBrewPlistNotLoaded(t *testing.T) {
	h := newFakeHost(t, "darwin")
	h.writeFile(t, "Library/LaunchAgents/homebrew.mxcl.cswap.plist")

	got, err := Status()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Homebrew service homebrew.mxcl.cswap: installed but not loaded") {
		t.Errorf("unexpected status:\n%s", got)
	}
}

func TestStatusNoServices(t *testing.T) {
	newFakeHost(t, "darwin")
	got, err := Status()
	if err != nil {
		t.Fatal(err)
	}
	want := "LaunchAgent " + Label + ": not installed\nHomebrew service: not installed"
	if got != want {
		t.Errorf("status = %q, want %q", got, want)
	}
}

func TestStatusReportsBrewSystemdUnit(t *testing.T) {
	h := newFakeHost(t, "linux")
	unit := h.writeFile(t, ".config/systemd/user/homebrew.cswap.service")
	h.replies["systemctl --user is-active homebrew.cswap.service"] = "active\n"

	got, err := Status()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"systemd user unit " + unitName + ": not installed",
		"Homebrew service homebrew.cswap.service: active",
		"unit: " + unit,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
}

func TestInstallRefusesWhenBrewServicePresent(t *testing.T) {
	for _, tc := range []struct {
		os, file, key, reply string
	}{
		{"darwin", "Library/LaunchAgents/homebrew.mxcl.cswap.plist", "", ""},
		{"linux", ".config/systemd/user/homebrew.cswap.service", "systemctl --user is-active homebrew.cswap.service", "active"},
	} {
		t.Run(tc.os, func(t *testing.T) {
			h := newFakeHost(t, tc.os)
			h.writeFile(t, tc.file)
			if tc.key != "" {
				h.replies[tc.key] = tc.reply
			}
			_, err := Install(nil)
			if err == nil || !strings.Contains(err.Error(), "brew services") {
				t.Fatalf("Install err = %v, want a refusal naming brew services", err)
			}
			for _, p := range []string{
				filepath.Join(h.home, "Library", "LaunchAgents", Label+".plist"),
				filepath.Join(h.home, ".config", "systemd", "user", unitName),
			} {
				if _, err := os.Stat(p); err == nil {
					t.Errorf("refused install still wrote %s", p)
				}
			}
			for _, c := range []string{"launchctl bootout", "launchctl bootstrap", "systemctl --user enable", "systemctl --user daemon-reload"} {
				if h.ran(c) {
					t.Errorf("refused install still ran %q", c)
				}
			}
		})
	}
}

func TestUninstallPointsAtBrewWhenOnlyBrewPresent(t *testing.T) {
	h := newFakeHost(t, "darwin")
	plist := h.writeFile(t, "Library/LaunchAgents/homebrew.mxcl.cswap.plist")
	h.replies[brewPrintKey()] = brewLaunchctlPrint

	msg, err := Uninstall()
	if err == nil {
		t.Fatalf("Uninstall claimed success (%q) with only the brew service present", msg)
	}
	if !strings.Contains(err.Error(), "brew services stop cswap") {
		t.Errorf("error does not say how to stop the brew service: %v", err)
	}
	if _, serr := os.Stat(plist); serr != nil {
		t.Errorf("uninstall touched the brew plist: %v", serr)
	}
	if h.ran("launchctl bootout") {
		t.Error("uninstall booted something out with only the brew service present")
	}
}

func TestUninstallOwnNotesRemainingBrewService(t *testing.T) {
	h := newFakeHost(t, "darwin")
	own := h.writeFile(t, "Library/LaunchAgents/"+Label+".plist")
	brew := h.writeFile(t, "Library/LaunchAgents/homebrew.mxcl.cswap.plist")

	msg, err := Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(own); !os.IsNotExist(serr) {
		t.Errorf("own plist not removed: %v", serr)
	}
	if _, serr := os.Stat(brew); serr != nil {
		t.Errorf("brew plist removed: %v", serr)
	}
	if !strings.Contains(msg, "Uninstalled LaunchAgent "+Label) || !strings.Contains(msg, "brew services stop cswap") {
		t.Errorf("unexpected message:\n%s", msg)
	}
	for _, c := range h.calls {
		if strings.Contains(c, "bootout") && strings.Contains(c, brewLaunchdLabel) {
			t.Errorf("uninstall booted out the brew service: %q", c)
		}
	}
}
