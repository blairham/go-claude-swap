// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/history"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// HistoryCommand shows the recorded switch history.
type HistoryCommand struct {
	UI  cli.Ui
	Now func() time.Time // nil = time.Now; tests pin it for --since
}

// HistoryFlags for cswap history.
type HistoryFlags struct {
	JSON  bool   `long:"json"  description:"Emit machine-readable JSON"`
	Limit int    `long:"limit" description:"Show at most this many of the newest switches (0 = all)"                                 default:"20"`
	Since string `long:"since" description:"Only switches since a duration ago (90m, 24h, 7d) or a date/time (2026-10-01, RFC 3339)"`
}

// Help text.
func (c *HistoryCommand) Help() string {
	return `Usage: cswap history [options]

Show recorded account switches, oldest first. Every completed switch is
recorded — manual (cswap switch, the TUI) and automatic (cswap auto) — with
its trigger, the outgoing account's utilization when known, and why.

The record lives in switch-history.jsonl under the backup root, one JSON
object per line, and is trimmed to its newest 1000 entries once it grows
past 512 KiB.

Options:
      --json         Emit machine-readable JSON
      --limit N      Show at most the newest N switches (default 20; 0 = all)
      --since WHEN   Only switches since WHEN: a duration ago (90m, 24h, 7d)
                     or a date/time (2026-10-01, 2026-10-01T09:00:00Z)

Examples:
  cswap history
  cswap history --since 7d --limit 0
  cswap history --json --limit 5
`
}

// Synopsis line.
func (c *HistoryCommand) Synopsis() string {
	return "Show the account switch history"
}

// Run executes the command.
func (c *HistoryCommand) Run(args []string) int {
	var opts HistoryFlags
	_, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	fail := func(kind string, err error) int {
		if opts.JSON {
			return jsonError(c.UI, kind, err)
		}
		c.UI.Error("Error: " + err.Error())
		return 1
	}

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	if opts.Limit < 0 {
		return fail("UsageError", errors.New("--limit must be 0 or more"))
	}
	var since time.Time
	if opts.Since != "" {
		var err error
		if since, err = parseSince(opts.Since, now()); err != nil {
			return fail("UsageError", err)
		}
	}

	recs, err := history.Query(since, opts.Limit)
	if err != nil {
		return fail("HistoryError", err)
	}

	if opts.JSON {
		if recs == nil {
			recs = []history.Record{}
		}
		return printJSON(c.UI, map[string]any{
			"path":     paths.HistoryPath(),
			"count":    len(recs),
			"switches": recs,
		})
	}

	if len(recs) == 0 {
		c.UI.Output("No switches recorded.")
		return 0
	}
	for _, r := range recs {
		c.UI.Output(humanRecord(r))
	}
	return 0
}

// humanRecord renders one record as a single line in local time.
func humanRecord(r history.Record) string {
	when := r.TS
	if t := r.Time(); !t.IsZero() {
		when = t.Local().Format("2006-01-02 15:04")
	}
	from := "unmanaged"
	if r.From != nil {
		from = fmt.Sprintf("Account-%d", r.From.Number)
	}
	line := fmt.Sprintf("%s  %s -> Account-%d (%s)  %s", when, from, r.To.Number, r.To.Email, r.Trigger)
	if r.Source != "" {
		line += " [" + r.Source + "]"
	}
	if r.ActiveUtilizationPct != nil {
		line += fmt.Sprintf("  %s%% used", strconv.FormatFloat(*r.ActiveUtilizationPct, 'f', -1, 64))
	}
	if r.Reason != "" {
		line += "  — " + r.Reason
	}
	return line
}

// parseSince accepts a Go duration, a whole-day count ("7d"), a date
// ("2006-01-02", local midnight), or an RFC 3339 instant.
func parseSince(s string, now time.Time) (time.Time, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.AddDate(0, 0, -n), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf(
		"invalid --since %q: use a duration (90m, 24h, 7d) or a date/time (2026-10-01, RFC 3339)",
		s,
	)
}
