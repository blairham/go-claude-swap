// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package notify posts desktop notifications: Notification Center on macOS
// through the pinned /usr/bin/osascript, notify-send on Linux when it is
// installed, and nothing anywhere else. Every spawn is bounded by a timeout
// so a wedged notification daemon can never stall the auto-switch loop.
package notify

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

// Timeout bounds one notification spawn.
const Timeout = 5 * time.Second

// osascriptPath is pinned rather than resolved from PATH: a launchd or
// Homebrew service runs with a minimal PATH, and the system binary is the
// one that is always there.
const osascriptPath = "/usr/bin/osascript"

// Notifier posts one notification.
type Notifier interface {
	Notify(title, body string) error
}

// Func adapts a function to Notifier.
type Func func(title, body string) error

// Notify implements Notifier.
func (f Func) Notify(title, body string) error { return f(title, body) }

// Nop discards every notification.
var Nop Notifier = Func(func(string, string) error { return nil })

// Desktop returns the notifier for this platform, or Nop when the platform
// has no supported mechanism.
func Desktop() Notifier {
	probe, _ := command(runtime.GOOS, "", "")
	if probe == "" {
		return Nop
	}
	if _, err := exec.LookPath(probe); err != nil {
		return Nop
	}
	return Func(func(title, body string) error {
		name, args := command(runtime.GOOS, title, body)
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		return exec.CommandContext(ctx, name, args...).Run()
	})
}

// command builds the spawn for goos, or "" when there is none. The title and
// body travel as arguments — never spliced into script source — so no text
// can break out of the AppleScript string.
func command(goos, title, body string) (string, []string) {
	switch goos {
	case "darwin":
		return osascriptPath, []string{
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run",
			title, body,
		}
	case "linux":
		return "notify-send", []string{"--app-name=cswap", title, body}
	default:
		return "", nil
	}
}
