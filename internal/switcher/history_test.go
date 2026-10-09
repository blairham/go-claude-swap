// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package switcher

import (
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/history"
	"github.com/blairham/go-claude-swap/internal/usage"
)

// twoAccounts adds a@b.co (slot 1) then b@b.co (slot 2), leaving b live.
func twoAccounts(t *testing.T) {
	t.Helper()
	home := env(t)
	t.Setenv("ANTHROPIC_MODEL", "")
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	login(t, home, "b@b.co", "", credJSON("at-b", "rt-b"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
}

func readHistory(t *testing.T) []history.Record {
	t.Helper()
	recs, err := history.Read()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestSwitchRecordsHistory(t *testing.T) {
	twoAccounts(t)
	// A decision-trusted cache row for the outgoing account supplies the
	// utilization a manual switch does not know itself.
	if err := usage.Update(func(s *usage.Store) {
		s.Put(2, &usage.Entry{
			Email: "b@b.co", FetchedAt: float64(time.Now().Unix()),
			LastGood: &usage.Usage{FiveHour: &usage.Window{Pct: 42}, SevenDay: &usage.Window{Pct: 17}},
		})
	}); err != nil {
		t.Fatal(err)
	}

	res, err := SwitchTo("1", false)
	if err != nil || !res.Switched {
		t.Fatalf("switch: %+v, %v", res, err)
	}
	recs := readHistory(t)
	if len(recs) != 1 {
		t.Fatalf("history = %+v", recs)
	}
	r := recs[0]
	if r.From == nil || r.From.Number != 2 || r.From.Email != "b@b.co" ||
		r.To.Number != 1 || r.To.Email != "a@b.co" || r.Trigger != "manual" || r.Source != "cli" {
		t.Errorf("record = %+v", r)
	}
	if r.ActiveUtilizationPct == nil || *r.ActiveUtilizationPct != 42 {
		t.Errorf("utilization = %v", r.ActiveUtilizationPct)
	}

	// A no-op switch is not a switch and leaves no record.
	if res, _ := SwitchTo("1", false); res.Switched {
		t.Fatalf("expected already-active, got %+v", res)
	}
	if n := len(readHistory(t)); n != 1 {
		t.Errorf("already-active recorded: %d records", n)
	}
}

func TestSwitchToFromRecordsOrigin(t *testing.T) {
	twoAccounts(t)
	util := 100.0
	if _, err := SwitchToFrom("1", false, Origin{
		Trigger: "at-limit", Source: "auto", Reason: "active account at its limit", ActiveUtilizationPct: &util,
	}); err != nil {
		t.Fatal(err)
	}
	recs := readHistory(t)
	if len(recs) != 1 {
		t.Fatalf("history = %+v", recs)
	}
	r := recs[0]
	if r.Trigger != "at-limit" || r.Source != "auto" || r.Reason != "active account at its limit" ||
		r.ActiveUtilizationPct == nil || *r.ActiveUtilizationPct != 100 {
		t.Errorf("record = %+v", r)
	}
}

func TestRotateRecordsHistory(t *testing.T) {
	twoAccounts(t)
	if _, err := Rotate(); err != nil {
		t.Fatal(err)
	}
	recs := readHistory(t)
	if len(recs) != 1 || recs[0].Trigger != "rotate" || recs[0].To.Number != 1 || recs[0].ActiveUtilizationPct != nil {
		t.Fatalf("history = %+v", recs)
	}
}
