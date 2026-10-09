// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/mappings"
)

// MapCommand maps a directory to an account, or lists the mappings.
type MapCommand struct {
	UI cli.Ui
}

// MapFlags for cswap map.
type MapFlags struct {
	JSON bool `long:"json" description:"Emit JSON"`
}

// Help text.
func (c *MapCommand) Help() string {
	return `Usage: cswap map [options] [<number|alias|email> [<path>]]

Map a stored account to a directory so 'cswap run' with no account launches
it there (and in every directory below it, unless a deeper mapping exists).
PATH defaults to the current directory. With no arguments, lists all
mappings.

Mappings live in mappings.json in the backup directory, shared with the
Python claude-swap, and are keyed by the account's identity (email plus
organization), not its slot number, so they survive 'cswap move'.

Examples:
  cswap map 2 ~/work/client-app
  cswap map user@example.com          # map the current directory
  cswap map                           # list all mappings

Options:
      --json  Emit JSON
`
}

// Synopsis line.
func (c *MapCommand) Synopsis() string {
	return "Map a directory to an account for 'cswap run'"
}

// Run executes the command.
func (c *MapCommand) Run(args []string) int {
	var opts MapFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	fail := func(err error) int {
		if opts.JSON {
			return jsonError(c.UI, "MappingError", err)
		}
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	if len(remaining) > 2 {
		c.UI.Error(c.Help())
		return 1
	}

	seq, err := account.Load()
	if err != nil {
		return fail(err)
	}
	store := mappings.Open()

	if len(remaining) == 0 {
		return c.list(store, seq, opts.JSON, fail)
	}

	slot, err := seq.Resolve(remaining[0])
	if err != nil {
		return fail(err)
	}
	a := seq.Get(slot)
	target := "."
	if len(remaining) == 2 {
		target = remaining[1]
	}
	if fi, serr := os.Stat(target); serr != nil || !fi.IsDir() {
		if !opts.JSON {
			c.UI.Warn(fmt.Sprintf("Warning: %s is not an existing directory (mapping it anyway)", target))
		}
	}
	key, prev, err := store.Set(target, a.Email, a.OrganizationUUID)
	if err != nil {
		return fail(err)
	}
	if opts.JSON {
		doc := map[string]any{
			"path":    key,
			"account": accountRefJSON(slot, a.Email),
		}
		if prev != nil {
			doc["previous"] = map[string]any{"email": prev.Email, "organizationUuid": prev.OrganizationUUID}
		}
		return printJSON(c.UI, doc)
	}
	line := fmt.Sprintf("Mapped %s → Account-%d (%s)", key, slot, a.Email)
	if prev != nil && prev.Email != a.Email {
		line += fmt.Sprintf(" (was %s)", prev.Email)
	}
	c.UI.Output(line)
	return 0
}

func (c *MapCommand) list(store *mappings.Store, seq *account.Sequence, asJSON bool, fail func(error) int) int {
	all, err := store.All()
	if err != nil {
		return fail(err)
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if asJSON {
		rows := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			e := all[k]
			row := map[string]any{
				"path":             k,
				"email":            e.Email,
				"organizationUuid": e.OrganizationUUID,
				"added":            e.Added,
				"number":           nil,
			}
			if slot := seq.FindByIdentity(e.Email, e.OrganizationUUID); slot != 0 {
				row["number"] = slot
			}
			rows = append(rows, row)
		}
		return printJSON(c.UI, map[string]any{"mappings": rows})
	}

	if len(keys) == 0 {
		c.UI.Output("No directory mappings yet.")
		c.UI.Output("Map one with: cswap map <number|alias|email> [path]")
		return 0
	}
	c.UI.Output("Directory mappings:")
	for _, k := range keys {
		e := all[k]
		if slot := seq.FindByIdentity(e.Email, e.OrganizationUUID); slot != 0 {
			c.UI.Output(fmt.Sprintf("  %s → %d: %s %s", k, slot, e.Email, orgTag(seq.Get(slot).OrganizationName)))
		} else {
			c.UI.Output(fmt.Sprintf("  %s → %s (account removed)", k, e.Email))
		}
	}
	return 0
}

// UnmapCommand removes a directory mapping.
type UnmapCommand struct {
	UI cli.Ui
}

// Help text.
func (c *UnmapCommand) Help() string {
	return `Usage: cswap unmap [options] [<path>]

Remove a directory → account mapping. PATH defaults to the current
directory. Only an exact mapping is removed; a mapping on a parent
directory is left alone.

Options:
      --json  Emit JSON
`
}

// Synopsis line.
func (c *UnmapCommand) Synopsis() string {
	return "Remove a directory → account mapping"
}

// Run executes the command.
func (c *UnmapCommand) Run(args []string) int {
	var opts MapFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	if len(remaining) > 1 {
		c.UI.Error(c.Help())
		return 1
	}
	target := "."
	if len(remaining) == 1 {
		target = remaining[0]
	}
	key, removed, err := mappings.Open().Remove(target)
	if err != nil {
		if opts.JSON {
			return jsonError(c.UI, "MappingError", err)
		}
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	if opts.JSON {
		return printJSON(c.UI, map[string]any{"path": key, "removed": removed})
	}
	if removed {
		c.UI.Output("Unmapped " + key)
	} else {
		c.UI.Output("No mapping for " + key)
	}
	return 0
}
