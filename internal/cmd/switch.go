package cmd

import (
	"fmt"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/switcher"
)

// SwitchCommand rotates to the next account or activates a specific one.
type SwitchCommand struct {
	UI cli.Ui
}

// SwitchFlags for cswap switch.
type SwitchFlags struct {
	Force bool `long:"force" description:"Skip the already-active guard and switch even when backups or token refresh fail"`
	JSON  bool `long:"json" description:"Emit machine-readable JSON"`
}

// Help text.
func (c *SwitchCommand) Help() string {
	return `Usage: cswap switch [options] [<number|alias|email>]

Switch the live Claude Code login. With no argument, rotates to the next
switchable account in slot order. With an argument, activates that account.

The switch refreshes the target's OAuth token first when it is stale (so
Claude Code never starts on a dead token and asks you to log in again),
preserves the outgoing account's credentials, splices only the
account-specific parts of ~/.claude.json, and holds Claude Code's own
credential locks so a concurrent token refresh cannot collide.

Options:
      --force  Skip the already-active guard; switch even when the outgoing
               backup or the target token refresh fails
      --json   Emit machine-readable JSON

Examples:
  cswap switch
  cswap switch 2
  cswap switch work@example.com
`
}

// Synopsis line.
func (c *SwitchCommand) Synopsis() string {
	return "Switch the active Claude account"
}

// Run executes the command.
func (c *SwitchCommand) Run(args []string) int {
	var opts SwitchFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}

	var res *switcher.Result
	var err error
	strategy := "rotation"
	if len(remaining) > 0 {
		strategy = "direct"
		res, err = switcher.SwitchTo(remaining[0], opts.Force)
	} else {
		res, err = switcher.Rotate()
	}
	if err != nil {
		if opts.JSON {
			return jsonError(c.UI, "SwitchError", err)
		}
		c.UI.Error("Error: " + err.Error())
		return 1
	}

	if opts.JSON {
		return printJSON(c.UI, map[string]any{
			"switched": res.Switched,
			"from":     accountRefJSON(res.FromSlot, res.FromEmail),
			"to":       accountRefJSON(res.ToSlot, res.ToEmail),
			"strategy": strategy,
			"reason":   res.Reason,
			"warnings": res.Warnings,
		})
	}

	for _, w := range res.Warnings {
		c.UI.Warn(w)
	}
	switch res.Reason {
	case "switched":
		c.UI.Output(fmt.Sprintf("Switched to Account-%d (%s)", res.ToSlot, res.ToEmail))
		c.UI.Output("Restart Claude Code to apply immediately — a running instance picks it up within ~30s.")
	case "already-active":
		c.UI.Output(fmt.Sprintf("Already on Account-%d (%s)", res.ToSlot, res.ToEmail))
	case "only-one-account":
		c.UI.Output("Only one account is managed; nothing to switch to.")
	case "no-valid-target":
		c.UI.Output("No switchable account found.")
		return 1
	default:
		c.UI.Output(res.Reason)
	}
	return 0
}
