// Package history keeps the structured switch record: one JSON object per
// line in switch-history.jsonl under the backup root, appended by every
// completed switch (manual, TUI, or auto) and trimmed to a bounded size.
//
// Writers are serialized by the caller — the switcher appends while it
// still holds cswap's own switch lock — so Append takes no lock of its own.
// Readers need none: appends are single small writes and a trim replaces the
// file atomically.
package history

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// Size bounds. Once the file grows past MaxBytes it is rewritten keeping
// only the newest KeepRecords entries. At a few hundred bytes per record
// that is roughly a thousand switches, months of history at observed rates.
// Variables so tests can shrink them.
var (
	MaxBytes    int64 = 512 << 10
	KeepRecords       = 1000
)

// Ref identifies an account in a record.
type Ref struct {
	Number int    `json:"number"`
	Email  string `json:"email"`
}

// Record is one completed switch.
type Record struct {
	TS      string `json:"ts"` // UTC, account.TimeFormat
	From    *Ref   `json:"from"`
	To      Ref    `json:"to"`
	Trigger string `json:"trigger"` // manual, rotate, proactive, at-limit, failover, consume-first
	Source  string `json:"source"`  // cli, tui, auto
	// ActiveUtilizationPct is the outgoing account's binding utilization at
	// switch time; nil when it was not known.
	ActiveUtilizationPct *float64 `json:"activeUtilizationPct"`
	Reason               string   `json:"reason,omitempty"`
}

// Time parses the record's timestamp; the zero time when malformed.
func (r Record) Time() time.Time {
	t, err := time.Parse(account.TimeFormat, r.TS)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Append writes rec as one line, stamping TS when empty, then trims the file
// if it has outgrown MaxBytes.
func Append(rec Record) error {
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(account.TimeFormat)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.BackupRoot(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(paths.HistoryPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(line, '\n'))
	info, serr := f.Stat()
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if serr == nil && info.Size() > MaxBytes {
		return trim()
	}
	return nil
}

// trim rewrites the file atomically with only the newest KeepRecords lines.
func trim() error {
	raw, err := os.ReadFile(paths.HistoryPath())
	if err != nil {
		return err
	}
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) > KeepRecords {
		lines = lines[len(lines)-KeepRecords:]
	}
	out := append(bytes.Join(lines, []byte("\n")), '\n')
	return account.WriteFileAtomic(paths.HistoryPath(), out, 0o600)
}

// Read returns every record in file (chronological) order. A missing file
// is an empty history; malformed lines are skipped rather than failing the
// whole read.
func Read() ([]Record, error) {
	f, err := os.Open(paths.HistoryPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.TS == "" {
			continue
		}
		recs = append(recs, r)
	}
	return recs, sc.Err()
}

// Query returns the records at or after since (zero = no bound), keeping
// only the newest limit of them (limit <= 0 = all), in chronological order.
func Query(since time.Time, limit int) ([]Record, error) {
	all, err := Read()
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, r := range all {
		if !since.IsZero() && r.Time().Before(since) {
			continue
		}
		out = append(out, r)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
