package cmd

import (
	"fmt"
	"time"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/settings"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

// StatusCommand shows the current account and its usage.
type StatusCommand struct {
	UI cli.Ui
}

// StatusFlags for cswap status.
type StatusFlags struct {
	JSON bool `long:"json" description:"Emit machine-readable JSON"`
}

// Help text.
func (c *StatusCommand) Help() string {
	return `Usage: cswap status [options]

Show the currently active Claude account and its usage.

Options:
  --json  Emit machine-readable JSON
`
}

// Synopsis line.
func (c *StatusCommand) Synopsis() string {
	return "Show the current account"
}

// Run executes the command.
func (c *StatusCommand) Run(args []string) int {
	var opts StatusFlags
	_, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}

	seq, err := account.Load()
	if err != nil {
		if opts.JSON {
			return jsonError(c.UI, "ConfigError", err)
		}
		c.UI.Error("Error: " + err.Error())
		return 1
	}

	id, _ := claudecfg.ReadIdentity()
	if id == nil {
		if opts.JSON {
			return printJSON(c.UI, map[string]any{"active": nil})
		}
		c.UI.Output("Status: No active Claude account")
		return 0
	}
	slot := seq.FindByIdentity(id.Email, id.OrganizationUUID)
	if slot == 0 {
		if opts.JSON {
			return printJSON(c.UI, map[string]any{
				"active": map[string]any{"email": id.Email, "managed": false},
			})
		}
		c.UI.Output(fmt.Sprintf("Status: %s (not managed)", id.Email))
		return 0
	}

	col := &switcher.Collector{Models: settings.Load().ResolvedModels()}
	snaps := col.Collect(seq)
	now := time.Now()
	var snap *switcher.Snapshot
	for i := range snaps {
		if snaps[i].Slot == slot {
			snap = &snaps[i]
			break
		}
	}

	a := seq.Get(slot)
	if opts.JSON {
		active := map[string]any{
			"number":           slot,
			"email":            a.Email,
			"organizationName": a.OrganizationName,
			"organizationUuid": a.OrganizationUUID,
			"isOrganization":   a.OrganizationUUID != "",
			"managed":          true,
		}
		if a.Alias != "" {
			active["alias"] = a.Alias
		}
		if snap != nil {
			active["usageStatus"] = statusJSON(snap.Status)
			active["usage"] = usageJSON(snap.Usage, now)
		}
		return printJSON(c.UI, map[string]any{
			"active":               active,
			"totalManagedAccounts": len(seq.Accounts),
		})
	}

	c.UI.Output(fmt.Sprintf("Status: Account-%d (%s %s)", slot, accountLabel(a.Alias, a.Email), orgTag(a.OrganizationName)))
	c.UI.Output(fmt.Sprintf("  Total managed accounts: %d", len(seq.Accounts)))
	if snap != nil && snap.Usage != nil {
		for _, l := range usageLines(snap.Usage, now) {
			c.UI.Output(l)
		}
	} else if snap != nil && sentinelNote(snap.Status) != "" {
		c.UI.Output("     └ " + sentinelNote(snap.Status))
	}
	return 0
}
