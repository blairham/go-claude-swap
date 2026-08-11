package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/hashicorp/cli"
	flags "github.com/jessevdk/go-flags"

	"github.com/blairham/go-claude-swap/internal/switcher"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// SchemaVersion of --json output envelopes.
const SchemaVersion = 1

// parseFlags parses args into opts, printing help on -h. Returns the
// remaining positional args and whether the caller should stop (help shown
// or error printed).
func parseFlags(ui cli.Ui, helpText string, opts any, args []string) ([]string, bool, int) {
	parser := flags.NewParser(opts, flags.Default&^flags.PrintErrors)
	remaining, err := parser.ParseArgs(args)
	if err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) && flagsErr.Type == flags.ErrHelp {
			ui.Output(helpText)
			return nil, true, 0
		}
		ui.Error(fmt.Sprintf("Error parsing flags: %v", err))
		return nil, true, 1
	}
	return remaining, false, 0
}

// printJSON emits a --json envelope with schemaVersion added.
func printJSON(ui cli.Ui, doc map[string]any) int {
	doc["schemaVersion"] = SchemaVersion
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		ui.Error(err.Error())
		return 1
	}
	ui.Output(string(data))
	return 0
}

// jsonError prints an error envelope on stdout (JSON mode) and returns 1.
func jsonError(ui cli.Ui, kind string, err error) int {
	printJSON(ui, map[string]any{
		"error": map[string]any{"type": kind, "message": err.Error()},
	})
	return 1
}

// accountLabel renders "alias (email)" or just the email.
func accountLabel(alias, email string) string {
	if alias != "" {
		return alias + " (" + email + ")"
	}
	return email
}

// orgTag renders the bracketed org tag: organization name or "personal".
func orgTag(orgName string) string {
	if orgName == "" {
		return "[personal]"
	}
	return "[" + orgName + "]"
}

// sentinelNote maps a usage status to the human explanation shown in list.
func sentinelNote(s switcher.UsageStatus) string {
	switch s {
	case switcher.StatusTokenExpired:
		return "token expired — refresh deferred this pass; retries automatically"
	case switcher.StatusAPIKey:
		return "API key (no quota)"
	case switcher.StatusKeychainUnavailable:
		return "keychain unavailable"
	case switcher.StatusReloginRequired:
		return "re-login needed — run 'cswap add' while logged in as this account"
	case switcher.StatusNoCredentials:
		return "no stored credentials — re-add with 'cswap add'"
	default:
		return ""
	}
}

// usageLines renders the tree-connector usage rows for one account.
func usageLines(u *usage.Usage, now time.Time) []string {
	if u == nil {
		return nil
	}
	type row struct {
		label, body string
	}
	var rows []row

	windowBody := func(pct float64, resetsAt string, marker string) string {
		ts := usage.ParseReset(resetsAt)
		if ts == 0 {
			return fmt.Sprintf("%3.0f%%%s", pct, marker)
		}
		clock := usage.FormatClock(ts, now)
		countdown := usage.FormatCountdown(time.Until(time.Unix(ts, 0)))
		return fmt.Sprintf("%3.0f%%   resets %-12s in %s%s", pct, clock, countdown, marker)
	}

	if s := u.Spend; s != nil {
		body := fmt.Sprintf("%3.0f%%", s.Pct)
		if ts := usage.ParseReset(s.ResetsAt); ts != 0 {
			body += fmt.Sprintf("   resets %-12s", usage.FormatClock(ts, now))
		}
		body += fmt.Sprintf("  $%.2f / $%.2f", s.Used, s.Limit)
		rows = append(rows, row{"$$", body})
	}
	if w := u.FiveHour; w != nil {
		rows = append(rows, row{"5h", windowBody(w.Pct, w.ResetsAt, "")})
	}
	if w := u.SevenDay; w != nil {
		rows = append(rows, row{"7d", windowBody(w.Pct, w.ResetsAt, "")})
	}
	for _, w := range u.Scoped {
		marker := ""
		if w.Pct >= 100 {
			marker = "  (!)"
		}
		rows = append(rows, row{w.Name, windowBody(w.Pct, w.ResetsAt, marker)})
	}
	if len(rows) == 0 {
		return nil
	}

	width := 0
	for _, r := range rows {
		if len(r.label) > width {
			width = len(r.label)
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		conn := "├"
		if i == len(rows)-1 {
			conn = "└"
		}
		out[i] = fmt.Sprintf("     %s %-*s  %s", conn, width+1, r.label+":", r.body)
	}
	return out
}

// formatAge renders "just now" / "12m ago" / "3h ago" / "2d ago".
func formatAge(seconds float64) string {
	if math.IsInf(seconds, 1) {
		return "never"
	}
	switch {
	case seconds < 60:
		return "just now"
	case seconds < 3600:
		return fmt.Sprintf("%dm ago", int(seconds/60))
	case seconds < 86400:
		return fmt.Sprintf("%dh ago", int(seconds/3600))
	default:
		return fmt.Sprintf("%dd ago", int(seconds/86400))
	}
}

// usageJSON renders the normalized usage for --json output (camelCase).
func usageJSON(u *usage.Usage, now time.Time) map[string]any {
	if u == nil {
		return nil
	}
	windowJSON := func(w *usage.Window) map[string]any {
		m := map[string]any{"pct": w.Pct}
		if ts := usage.ParseReset(w.ResetsAt); ts != 0 {
			m["resetsAt"] = w.ResetsAt
			m["countdown"] = usage.FormatCountdown(time.Until(time.Unix(ts, 0)))
			m["clock"] = usage.FormatClock(ts, now)
		}
		if w.Name != "" {
			m["name"] = w.Name
		}
		return m
	}
	doc := map[string]any{}
	if u.FiveHour != nil {
		doc["fiveHour"] = windowJSON(u.FiveHour)
	}
	if u.SevenDay != nil {
		doc["sevenDay"] = windowJSON(u.SevenDay)
	}
	if u.Spend != nil {
		s := map[string]any{
			"used": u.Spend.Used, "limit": u.Spend.Limit,
			"pct": u.Spend.Pct, "currency": u.Spend.Currency,
		}
		if u.Spend.ResetsAt != "" {
			s["resetsAt"] = u.Spend.ResetsAt
		}
		doc["spend"] = s
	}
	if len(u.Scoped) > 0 {
		var scoped []map[string]any
		for i := range u.Scoped {
			scoped = append(scoped, windowJSON(&u.Scoped[i]))
		}
		doc["scoped"] = scoped
	}
	return doc
}

// accountRefJSON is the {number, email} shape used in switch envelopes.
func accountRefJSON(slot int, email string) map[string]any {
	if slot == 0 && email == "" {
		return nil
	}
	return map[string]any{"number": slot, "email": email}
}
