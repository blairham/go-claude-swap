package cmd

import (
	"fmt"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/switcher"
)

// ExportCommand writes accounts to a portable file.
type ExportCommand struct {
	UI cli.Ui
}

// ExportFlags for cswap export.
type ExportFlags struct {
	Account string `long:"account" description:"Export only this account (number, alias, or email)"`
	Full    bool   `long:"full" description:"Keep whole credential/config blobs (same-machine backup)"`
}

// Help text.
func (c *ExportCommand) Help() string {
	return `Usage: cswap export [options] <path|->

Export managed accounts to a JSON file (or stdout with "-"). The export is
NOT encrypted — pipe it through gpg for transport. By default, credentials
and configs are slimmed to the account-specific parts; --full keeps whole
blobs for a same-machine backup.

Options:
      --account SEL  Export only this account
      --full         Keep whole credential/config blobs
`
}

// Synopsis line.
func (c *ExportCommand) Synopsis() string {
	return "Export accounts to a file"
}

// Run executes the command.
func (c *ExportCommand) Run(args []string) int {
	var opts ExportFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	if len(remaining) != 1 {
		c.UI.Error("Usage: cswap export <path|->")
		return 1
	}
	n, err := switcher.Export(remaining[0], Version, opts.Full, opts.Account)
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	c.UI.Error(fmt.Sprintf("Exported %d account(s). The file contains live credentials — protect it.", n))
	return 0
}

// ImportCommand reads accounts from an export file.
type ImportCommand struct {
	UI cli.Ui
}

// ImportFlags for cswap import.
type ImportFlags struct {
	Force bool `long:"force" description:"Overwrite existing accounts"`
}

// Help text.
func (c *ImportCommand) Help() string {
	return `Usage: cswap import [options] <path>

Import accounts from a cswap export file. Existing accounts are skipped
unless --force, or unless their stored token is dead (auto-heal).

Options:
      --force  Overwrite existing accounts
`
}

// Synopsis line.
func (c *ImportCommand) Synopsis() string {
	return "Import accounts from a file"
}

// Run executes the command.
func (c *ImportCommand) Run(args []string) int {
	var opts ImportFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	if len(remaining) != 1 {
		c.UI.Error("Usage: cswap import <path>")
		return 1
	}
	res, err := switcher.Import(remaining[0], opts.Force)
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	for _, w := range res.Warnings {
		c.UI.Warn(w)
	}
	c.UI.Output(fmt.Sprintf("Imported %d, overwrote %d, skipped %d", res.Imported, res.Overwritten, res.Skipped))
	return 0
}

// UnclaimedCommand lists or purges stashed credentials.
type UnclaimedCommand struct {
	UI cli.Ui
}

// UnclaimedFlags for cswap unclaimed.
type UnclaimedFlags struct {
	Purge string `long:"purge" description:"Delete the stash entry with this ID"`
}

// Help text.
func (c *UnclaimedCommand) Help() string {
	return `Usage: cswap unclaimed [options]

List credentials that were displaced by a switch and stashed rather than
discarded. Recover one by logging in with /login and running 'cswap add'.

Options:
      --purge ID  Delete a stash entry
`
}

// Synopsis line.
func (c *UnclaimedCommand) Synopsis() string {
	return "List stashed (unclaimed) credentials"
}

// Run executes the command.
func (c *UnclaimedCommand) Run(args []string) int {
	var opts UnclaimedFlags
	_, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	if opts.Purge != "" {
		if err := switcher.PurgeUnclaimed(opts.Purge); err != nil {
			c.UI.Error("Error: " + err.Error())
			return 1
		}
		c.UI.Output("Purged " + opts.Purge)
		return 0
	}
	entries, err := switcher.ListUnclaimed()
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	if len(entries) == 0 {
		c.UI.Output("No unclaimed credential entries")
		return 0
	}
	for _, e := range entries {
		line := e.ID
		if e.Slot != "" && e.Slot != "0" {
			line += "  slot " + e.Slot
		}
		if e.Reason != "" {
			line += "  " + e.Reason
		}
		c.UI.Output(line)
	}
	return 0
}
