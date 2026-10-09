// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"time"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/service"
	"github.com/blairham/go-claude-swap/pkg/swapapi"
)

// probeControl asks the control socket whether a live `cswap auto` loop is
// serving it. A variable so tests need no real socket.
var probeControl = func() (*swapapi.GetStatusResponse, bool) {
	return swapapi.Probe(time.Second)
}

// wakeEngine tells a running `cswap auto` loop that the roster changed, so
// an account that just became usable is considered now rather than after the
// loop's current sleep — which can be an hour when every account is
// exhausted. Silent when no loop is running. A variable so tests need no
// real socket.
var wakeEngine = func() {
	swapapi.Wake(time.Second)
}

// controlSocketLine reports whether a loop answers on the control socket —
// whichever service (or terminal) started it.
func controlSocketLine() string {
	st, ok := probeControl()
	if !ok {
		return "Control socket " + paths.SocketPath() + ": no answer (no cswap auto loop is serving it)"
	}
	line := fmt.Sprintf(
		"Control socket %s: answering (cswap %s, strategy %s",
		paths.SocketPath(),
		st.GetVersion(),
		st.GetStrategy(),
	)
	if st.GetStartedAtUnix() > 0 {
		line += ", up since " + time.Unix(st.GetStartedAtUnix(), 0).Format(time.RFC3339)
	}
	if st.GetDryRun() {
		line += ", dry run"
	}
	return line + ")"
}

// ServiceCommand manages the always-on auto-switch service.
type ServiceCommand struct {
	UI cli.Ui
}

// Help text.
func (c *ServiceCommand) Help() string {
	return `Usage: cswap service <install|uninstall|status> [-- <auto flags>]

Run 'cswap auto' as a login service that starts on boot and restarts on
crash: a launchd LaunchAgent on macOS, a systemd user unit on Linux.

If cswap was installed with Homebrew, 'brew services start cswap' runs the
same loop. status reports both; install refuses while the Homebrew service
is set up, and uninstall never touches it (use 'brew services stop cswap').

Flags after "--" are passed to 'cswap auto' (e.g. --threshold 85).
The service runs as your user (not root) so it can reach the Keychain and
your Claude Code config.

Examples:
  cswap service install
  cswap service install -- --strategy consume-first
  cswap service status
  cswap service uninstall
`
}

// Synopsis line.
func (c *ServiceCommand) Synopsis() string {
	return "Run auto-switch as an always-on login service"
}

// Run executes the command.
func (c *ServiceCommand) Run(args []string) int {
	if len(args) < 1 {
		c.UI.Error(c.Help())
		return 1
	}
	action := args[0]
	extra := args[1:]
	if len(extra) > 0 && extra[0] == "--" {
		extra = extra[1:]
	}

	var msg string
	var err error
	switch action {
	case "install":
		msg, err = service.Install(extra)
	case "uninstall":
		msg, err = service.Uninstall()
	case "status":
		msg, err = service.Status()
		if err == nil {
			msg += "\n" + controlSocketLine()
		}
	default:
		c.UI.Error(c.Help())
		return 1
	}
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	c.UI.Output(msg)
	return 0
}
