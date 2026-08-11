package cmd

import (
	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/tui"
)

// TUICommand opens the interactive dashboard.
type TUICommand struct {
	UI   cli.Ui
	Page string // "dashboard" or "watch"
}

// Help text.
func (c *TUICommand) Help() string {
	if c.Page == "watch" {
		return `Usage: cswap watch

Open the live dashboard on the watch page: all accounts with usage bars,
refreshing continuously. Press s to arm a switch, q to quit.
`
	}
	return `Usage: cswap tui

Open the interactive dashboard: live usage bars for every account, with
switching from the keyboard. Press ? keys: s switch, w watch, q quit.
`
}

// Synopsis line.
func (c *TUICommand) Synopsis() string {
	if c.Page == "watch" {
		return "Watch all accounts' usage live"
	}
	return "Open the interactive dashboard"
}

// Run executes the command.
func (c *TUICommand) Run(_ []string) int {
	if err := tui.Run(c.Page); err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	return 0
}
