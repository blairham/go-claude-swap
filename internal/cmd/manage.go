package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

// AddCommand captures the current login as a managed account.
type AddCommand struct {
	UI cli.Ui
}

// AddFlags for cswap add.
type AddFlags struct {
	Slot  int    `long:"slot" description:"Slot number to use (default: next free)"`
	Alias string `long:"alias" description:"Alias for the account"`
}

// Help text.
func (c *AddCommand) Help() string {
	return `Usage: cswap add [options]

Back up the current Claude Code login as a managed account. If the account
is already managed, its stored credentials are refreshed in place.

Options:
      --slot NUM    Slot number to use (default: next free)
      --alias NAME  Alias for the account (lowercase, [a-z0-9_.-])
`
}

// Synopsis line.
func (c *AddCommand) Synopsis() string {
	return "Register the current Claude Code login"
}

// Run executes the command.
func (c *AddCommand) Run(args []string) int {
	var opts AddFlags
	_, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	slot, email, err := switcher.Add(opts.Slot, opts.Alias)
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	c.UI.Output(fmt.Sprintf("Saved Account-%d (%s)", slot, email))
	return 0
}

// RemoveCommand deletes a managed account.
type RemoveCommand struct {
	UI cli.Ui
}

// RemoveFlags for cswap remove.
type RemoveFlags struct {
	Yes bool `short:"y" long:"yes" description:"Skip the confirmation prompt"`
}

// Help text.
func (c *RemoveCommand) Help() string {
	return `Usage: cswap remove [options] <number|alias|email>

Remove a managed account. Its stored credentials and config backup are
deleted; the account itself is not logged out anywhere.

Options:
  -y, --yes  Skip the confirmation prompt
`
}

// Synopsis line.
func (c *RemoveCommand) Synopsis() string {
	return "Remove a managed account"
}

// Run executes the command.
func (c *RemoveCommand) Run(args []string) int {
	var opts RemoveFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	if len(remaining) != 1 {
		c.UI.Error("Usage: cswap remove <number|alias|email>")
		return 1
	}
	if !opts.Yes {
		answer, err := c.UI.Ask(fmt.Sprintf("Remove %s and delete its stored credentials? [y/N]", remaining[0]))
		if err != nil || !strings.EqualFold(strings.TrimSpace(answer), "y") {
			c.UI.Output("Canceled.")
			return 0
		}
	}
	slot, email, err := switcher.Remove(remaining[0])
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	c.UI.Output(fmt.Sprintf("Removed Account-%d (%s)", slot, email))
	return 0
}

// DisableCommand holds an account out of rotation (or returns it).
type DisableCommand struct {
	UI      cli.Ui
	Disable bool
}

// Help text.
func (c *DisableCommand) Help() string {
	verb := "disable"
	if !c.Disable {
		verb = "enable"
	}
	return fmt.Sprintf(`Usage: cswap %s <number|alias|email>

%s an account for auto-rotation. Disabled accounts stay managed and remain
valid explicit switch targets.
`, verb, strings.ToUpper(verb[:1])+verb[1:])
}

// Synopsis line.
func (c *DisableCommand) Synopsis() string {
	if c.Disable {
		return "Hold an account out of auto-rotation"
	}
	return "Return an account to auto-rotation"
}

// Run executes the command.
func (c *DisableCommand) Run(args []string) int {
	if len(args) != 1 {
		c.UI.Error(c.Help())
		return 1
	}
	slot, email, err := switcher.SetDisabled(args[0], c.Disable)
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	state := "enabled"
	if c.Disable {
		state = "disabled"
	}
	c.UI.Output(fmt.Sprintf("Account-%d (%s) %s", slot, email, state))
	return 0
}

// AliasCommand sets, clears, or lists aliases.
type AliasCommand struct {
	UI cli.Ui
}

// AliasFlags for cswap alias.
type AliasFlags struct {
	Unset bool `long:"unset" description:"Remove the account's alias"`
}

// Help text.
func (c *AliasCommand) Help() string {
	return `Usage: cswap alias [options] [<number|email>] [<name>]

Set or remove an account's alias, or list all aliases with no arguments.
Aliases are lowercase ([a-z0-9_.-]), not purely numeric.

Options:
      --unset  Remove the account's alias
`
}

// Synopsis line.
func (c *AliasCommand) Synopsis() string {
	return "Manage account aliases"
}

// Run executes the command.
func (c *AliasCommand) Run(args []string) int {
	var opts AliasFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	switch {
	case len(remaining) == 0:
		seq, err := account.Load()
		if err != nil {
			c.UI.Error("Error: " + err.Error())
			return 1
		}
		c.UI.Output("Aliases:")
		for _, slot := range seq.Order {
			if a := seq.Get(slot); a != nil && a.Alias != "" {
				c.UI.Output(fmt.Sprintf("  %d: %s (%s)", slot, a.Alias, a.Email))
			}
		}
		return 0
	case opts.Unset && len(remaining) == 1:
		slot, email, err := switcher.SetAlias(remaining[0], "")
		if err != nil {
			c.UI.Error("Error: " + err.Error())
			return 1
		}
		c.UI.Output(fmt.Sprintf("Alias removed from Account-%d (%s)", slot, email))
		return 0
	case len(remaining) == 2:
		slot, email, err := switcher.SetAlias(remaining[0], remaining[1])
		if err != nil {
			c.UI.Error("Error: " + err.Error())
			return 1
		}
		c.UI.Output(fmt.Sprintf("Account-%d (%s) aliased as %q", slot, email, remaining[1]))
		return 0
	default:
		c.UI.Error(c.Help())
		return 1
	}
}

// MoveCommand relocates an account to a slot (swapping when occupied).
type MoveCommand struct {
	UI cli.Ui
}

// Help text.
func (c *MoveCommand) Help() string {
	return `Usage: cswap move <number|alias|email> <slot>

Move an account to a slot number. If the slot is occupied, the two accounts
swap places.
`
}

// Synopsis line.
func (c *MoveCommand) Synopsis() string {
	return "Move an account to a different slot"
}

// Run executes the command.
func (c *MoveCommand) Run(args []string) int {
	if len(args) != 2 {
		c.UI.Error(c.Help())
		return 1
	}
	target, err := strconv.Atoi(args[1])
	if err != nil {
		c.UI.Error("Error: target slot must be a number")
		return 1
	}
	swapped, otherEmail, err := switcher.Move(args[0], target)
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	if swapped {
		c.UI.Output(fmt.Sprintf("Moved %s to slot %d (swapped with %s)", args[0], target, otherEmail))
	} else {
		c.UI.Output(fmt.Sprintf("Moved %s to slot %d", args[0], target))
	}
	return 0
}
