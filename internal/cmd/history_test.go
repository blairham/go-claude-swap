// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/history"
)

func historyEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CSWAP_DISABLE_KEYCHAIN", "1")
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"90m":                  now.Add(-90 * time.Minute),
		"24h":                  now.Add(-24 * time.Hour),
		"7d":                   now.AddDate(0, 0, -7),
		"2026-10-01T09:00:00Z": time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		"2026-10-01":           time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local),
	}
	for in, want := range cases {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "yesterday", "-3d", "-1h"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("parseSince(%q) accepted", bad)
		}
	}
}

func TestHistoryCommandJSON(t *testing.T) {
	historyEnv(t)
	// Far from the real clock, so --since must be measured from c.Now.
	now := time.Date(2020, 3, 8, 12, 0, 0, 0, time.UTC)
	for i, ago := range []time.Duration{72 * time.Hour, 3 * time.Hour, 2 * time.Hour, time.Hour} {
		util := float64(90 + i)
		if err := history.Append(history.Record{
			TS:   now.Add(-ago).Format("2006-01-02T15:04:05Z"),
			From: &history.Ref{Number: 1, Email: "a@b.co"}, To: history.Ref{Number: i + 2, Email: "x@b.co"},
			Trigger: "proactive", Source: "auto", ActiveUtilizationPct: &util,
		}); err != nil {
			t.Fatal(err)
		}
	}
	ui := cli.NewMockUi()
	c := &HistoryCommand{UI: ui, Now: func() time.Time { return now }}
	if code := c.Run([]string{"--json", "--since", "1d", "--limit", "2"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ui.ErrorWriter.String())
	}
	var doc struct {
		SchemaVersion int              `json:"schemaVersion"`
		Count         int              `json:"count"`
		Switches      []history.Record `json:"switches"`
	}
	if err := json.Unmarshal(ui.OutputWriter.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, ui.OutputWriter.String())
	}
	if doc.SchemaVersion != SchemaVersion || doc.Count != 2 || len(doc.Switches) != 2 ||
		doc.Switches[0].To.Number != 4 || doc.Switches[1].To.Number != 5 {
		t.Errorf("doc = %+v", doc)
	}
}

func TestHistoryCommandHumanAndEmpty(t *testing.T) {
	historyEnv(t)
	ui := cli.NewMockUi()
	if code := (&HistoryCommand{UI: ui}).Run(
		nil,
	); code != 0 ||
		!strings.Contains(ui.OutputWriter.String(), "No switches recorded") {
		t.Fatalf("empty: %d %q", code, ui.OutputWriter.String())
	}
	if code := (&HistoryCommand{UI: cli.NewMockUi()}).Run([]string{"--since", "soon"}); code != 1 {
		t.Errorf("bad --since exit %d", code)
	}

	util := 100.0
	history.Append(history.Record{
		To: history.Ref{Number: 3, Email: "c@b.co"}, From: &history.Ref{Number: 2, Email: "b@b.co"},
		Trigger: "at-limit", Source: "auto", ActiveUtilizationPct: &util, Reason: "active account at its limit",
	})
	ui = cli.NewMockUi()
	(&HistoryCommand{UI: ui}).Run(nil)
	out := ui.OutputWriter.String()
	for _, want := range []string{"Account-2 -> Account-3 (c@b.co)", "at-limit [auto]", "100% used", "active account at its limit"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
