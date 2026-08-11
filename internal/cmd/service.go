package cmd

import (
	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/service"
)

// ServiceCommand manages the always-on auto-switch service.
type ServiceCommand struct {
	UI cli.Ui
}

// Help text.
func (c *ServiceCommand) Help() string {
	return `Usage: cswap service <install|uninstall|status> [-- <auto flags>]

Run 'cswap auto' as a login service that starts on boot and restarts on
crash: a launchd LaunchAgent on macOS, a systemd user unit on Linux.

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
