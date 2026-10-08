package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blairham/go-claude-swap/internal/paths"
)

func env(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
}

func TestAppendReadRoundTrip(t *testing.T) {
	env(t)
	util := 93.5
	if err := Append(Record{
		From: &Ref{Number: 1, Email: "a@b.co"}, To: Ref{Number: 2, Email: "b@b.co"},
		Trigger: "proactive", Source: "auto", ActiveUtilizationPct: &util, Reason: "r",
	}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Record{To: Ref{Number: 1, Email: "a@b.co"}, Trigger: "manual", Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	recs, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records", len(recs))
	}
	r := recs[0]
	if r.From == nil || r.From.Number != 1 || r.To.Number != 2 || r.Trigger != "proactive" ||
		r.ActiveUtilizationPct == nil || *r.ActiveUtilizationPct != 93.5 || r.TS == "" || r.Time().IsZero() {
		t.Errorf("first record = %+v", r)
	}
	if recs[1].From != nil || recs[1].ActiveUtilizationPct != nil {
		t.Errorf("second record = %+v", recs[1])
	}
	info, err := os.Stat(paths.HistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}

func TestReadMissingAndMalformed(t *testing.T) {
	env(t)
	recs, err := Read()
	if err != nil || recs != nil {
		t.Fatalf("missing file: %v, %v", recs, err)
	}
	os.MkdirAll(paths.BackupRoot(), 0o700)
	body := "not json\n" + `{"ts":"2026-10-01T00:00:00Z","to":{"number":2,"email":"b"},"trigger":"manual"}` + "\n{}\n"
	os.WriteFile(paths.HistoryPath(), []byte(body), 0o600)
	recs, err = Read()
	if err != nil || len(recs) != 1 || recs[0].To.Number != 2 {
		t.Fatalf("malformed lines: %+v, %v", recs, err)
	}
}

func TestAppendTrimsToNewest(t *testing.T) {
	env(t)
	oldMax, oldKeep := MaxBytes, KeepRecords
	t.Cleanup(func() { MaxBytes, KeepRecords = oldMax, oldKeep })
	MaxBytes, KeepRecords = 2000, 5

	for i := 1; i <= 40; i++ {
		if err := Append(Record{To: Ref{Number: i, Email: "x@y.z"}, Trigger: "manual"}); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(paths.HistoryPath())
		if info.Size() > MaxBytes {
			t.Fatalf("after %d appends size %d exceeds bound %d", i, info.Size(), MaxBytes)
		}
	}
	recs, _ := Read()
	if len(recs) == 0 || len(recs) > 40 {
		t.Fatalf("records = %d", len(recs))
	}
	if last := recs[len(recs)-1]; last.To.Number != 40 {
		t.Errorf("newest record lost: %+v", last)
	}
	raw, _ := os.ReadFile(paths.HistoryPath())
	if strings.Contains(string(raw), `"number":1,`) {
		t.Error("oldest record survived the trim")
	}
}

func TestQuerySinceAndLimit(t *testing.T) {
	env(t)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		ts := base.Add(time.Duration(i) * time.Hour).Format("2006-01-02T15:04:05Z")
		if err := Append(Record{TS: ts, To: Ref{Number: i + 1}, Trigger: "manual"}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Query(base.Add(2*time.Hour), 0)
	if len(got) != 3 || got[0].To.Number != 3 {
		t.Errorf("since: %+v", got)
	}
	got, _ = Query(time.Time{}, 2)
	if len(got) != 2 || got[0].To.Number != 4 || got[1].To.Number != 5 {
		t.Errorf("limit keeps newest: %+v", got)
	}
}
