package cmd

import (
	"fmt"
	"math"
	"time"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/settings"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

// ListCommand shows every managed account with usage.
type ListCommand struct {
	UI cli.Ui
}

// ListFlags for cswap list.
type ListFlags struct {
	JSON bool `long:"json" description:"Emit machine-readable JSON"`
}

// Help text.
func (c *ListCommand) Help() string {
	return `Usage: cswap list [options]

List every managed account with its rate-limit usage and reset times.

Options:
  --json  Emit machine-readable JSON
`
}

// Synopsis line.
func (c *ListCommand) Synopsis() string {
	return "List managed accounts with usage"
}

// Run executes the command.
func (c *ListCommand) Run(args []string) int {
	var opts ListFlags
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

	col := &switcher.Collector{Models: settings.Load().ResolvedModels()}
	snaps := col.Collect(seq)
	now := time.Now()

	if opts.JSON {
		return c.runJSON(seq, snaps, now)
	}

	if len(snaps) == 0 {
		c.UI.Output("No accounts are managed yet. Log in with Claude Code, then run 'cswap add'.")
		return 0
	}

	c.UI.Output("Accounts:")
	for _, s := range snaps {
		a := s.Account
		line := fmt.Sprintf("  %d: %s %s", s.Slot, accountLabel(a.Alias, a.Email), orgTag(a.OrganizationName))
		if s.Active {
			line += " (active)"
		}
		if a.Disabled {
			line += " (disabled)"
		}
		c.UI.Output(line)

		switch {
		case s.Usage != nil:
			lines := usageLines(s.Usage, now)
			if s.Age > 180 && len(lines) > 0 {
				lines[len(lines)-1] += " · " + formatAge(s.Age)
			}
			for _, l := range lines {
				c.UI.Output(l)
			}
		case sentinelNote(s.Status) != "":
			c.UI.Output("     └ " + sentinelNote(s.Status))
			if s.LastGood != nil {
				if h, ok := s.LastGood.Headroom(nil); ok {
					c.UI.Output(fmt.Sprintf("     └ last seen %.0f%% used · %s", 100-h, formatAge(s.Age)))
				}
			}
		default:
			note := "usage unavailable"
			if s.LastErr != "" {
				note += " (" + s.LastErr + ")"
			}
			c.UI.Output("     └ " + note)
		}
		c.UI.Output("")
	}
	return 0
}

func (c *ListCommand) runJSON(seq *account.Sequence, snaps []switcher.Snapshot, now time.Time) int {
	accounts := []map[string]any{}
	for _, s := range snaps {
		a := s.Account
		row := map[string]any{
			"number":           s.Slot,
			"email":            a.Email,
			"organizationName": a.OrganizationName,
			"organizationUuid": a.OrganizationUUID,
			"isOrganization":   a.OrganizationUUID != "",
			"active":           s.Active,
			"usageStatus":      statusJSON(s.Status),
			"usage":            usageJSON(s.Usage, now),
		}
		if a.Alias != "" {
			row["alias"] = a.Alias
		}
		if a.Disabled {
			row["disabled"] = true
		}
		if s.Usage != nil && !math.IsInf(s.Age, 1) {
			row["usageAgeSeconds"] = s.Age
		}
		if s.Usage == nil && s.LastGood != nil {
			row["lastGoodUsage"] = usageJSON(s.LastGood, now)
			if !math.IsInf(s.Age, 1) {
				row["lastGoodAgeSeconds"] = s.Age
			}
		}
		accounts = append(accounts, row)
	}
	doc := map[string]any{"accounts": accounts}
	if seq.ActiveAccountNumber != nil {
		doc["activeAccountNumber"] = *seq.ActiveAccountNumber
	} else {
		doc["activeAccountNumber"] = nil
	}
	return printJSON(c.UI, doc)
}

func statusJSON(s switcher.UsageStatus) string {
	return string(s)
}
