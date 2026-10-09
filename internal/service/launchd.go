// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blairham/go-claude-swap/internal/account"
)

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

// RenderPlist produces the LaunchAgent definition. RunAtLoad starts the
// loop at login (which follows every reboot); KeepAlive restarts it if it
// exits for any reason.
func RenderPlist(exe string, extraArgs []string) string {
	// --log-file makes the loop own (and rotate) the log; launchd's own
	// redirect below still catches anything printed before it is open.
	args := append([]string{exe, "auto", "--log-file", LogPath()}, extraArgs...)
	var sb strings.Builder
	sb.WriteString(xml.Header)
	sb.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(Label) + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range args {
		sb.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	sb.WriteString(`	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>30</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(LogPath()) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(LogPath()) + `</string>
</dict>
</plist>
`)
	return sb.String()
}

func xmlEscape(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

func installLaunchd(exe string, extraArgs []string) (string, error) {
	path := plistPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Stop a previous generation first so launchd reloads the new plist.
	_, _ = run("launchctl", "bootout", domainTarget()+"/"+Label)

	if err := account.WriteFileAtomic(path, []byte(RenderPlist(exe, extraArgs)), 0o644); err != nil {
		return "", err
	}
	// Modern interface first; fall back to the legacy one for older macOS.
	if _, err := run("launchctl", "bootstrap", domainTarget(), path); err != nil {
		if _, lerr := run("launchctl", "load", "-w", path); lerr != nil {
			return "", fmt.Errorf("launchctl could not load %s: %w", path, err)
		}
	}
	return fmt.Sprintf(
		"Installed LaunchAgent %s\n  plist: %s\n  log:   %s\nIt runs at login, restarts on crash, and survives reboots.",
		Label,
		path,
		LogPath(),
	), nil
}

func uninstallLaunchd() (string, error) {
	path := plistPath()
	_, _ = run("launchctl", "bootout", domainTarget()+"/"+Label)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return "Uninstalled LaunchAgent " + Label, nil
}

func statusLaunchd() (string, error) {
	if _, err := os.Stat(plistPath()); err != nil {
		return "not installed", nil
	}
	out, err := run("launchctl", "print", domainTarget()+"/"+Label)
	if err != nil {
		return "installed but not loaded (log: " + LogPath() + ")", nil
	}
	return launchdSummary(out), nil
}

func domainTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}
