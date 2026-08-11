// Package account models the sequence.json roster: the set of captured
// accounts, their slot numbers, aliases, and which one is active.
package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// TimeFormat is the UTC timestamp format used across all cswap files.
const TimeFormat = "2006-01-02T15:04:05Z"

// Account is one captured login.
type Account struct {
	Email            string `json:"email"`
	UUID             string `json:"uuid"`
	OrganizationUUID string `json:"organizationUuid"`
	OrganizationName string `json:"organizationName"`
	Added            string `json:"added"`
	Alias            string `json:"alias,omitempty"`
	Disabled         bool   `json:"disabled,omitempty"`
}

// Sequence is the roster document (sequence.json).
type Sequence struct {
	ActiveAccountNumber *int                `json:"activeAccountNumber"`
	LastUpdated         string              `json:"lastUpdated"`
	Order               []int               `json:"sequence"`
	Accounts            map[string]*Account `json:"accounts"`
}

var aliasRe = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// ValidateAlias enforces claude-swap's alias rules: lowercase slug, not
// purely digits (would collide with slot numbers), no leading dash.
func ValidateAlias(alias string) error {
	if alias == "" || !aliasRe.MatchString(alias) {
		return fmt.Errorf("alias must match %s", aliasRe.String())
	}
	if _, err := strconv.Atoi(alias); err == nil {
		return errors.New("alias cannot be purely numeric")
	}
	if alias[0] == '-' {
		return errors.New("alias cannot start with '-'")
	}
	return nil
}

// Load reads sequence.json. A missing file yields an empty roster; a
// present-but-unparseable file is an error (never rebuild over live data).
func Load() (*Sequence, error) {
	raw, err := os.ReadFile(paths.SequencePath())
	if errors.Is(err, os.ErrNotExist) {
		return &Sequence{Accounts: map[string]*Account{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", paths.SequencePath(), err)
	}
	var seq Sequence
	if err := json.Unmarshal(raw, &seq); err != nil {
		return nil, fmt.Errorf("corrupt roster %s (refusing to rebuild): %w", paths.SequencePath(), err)
	}
	if seq.Accounts == nil {
		seq.Accounts = map[string]*Account{}
	}
	return &seq, nil
}

// Save writes sequence.json atomically: temp file in the same directory,
// validate by re-reading, chmod before the rename commits it.
func (s *Sequence) Save() error {
	s.LastUpdated = time.Now().UTC().Format(TimeFormat)
	sort.Ints(s.Order)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(paths.SequencePath(), data, 0o600)
}

// WriteFileAtomic writes data via a same-directory temp file and rename.
// Mode is applied to the temp file so the rename is the final commit.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// NextSlot returns the lowest unused slot number, starting at 1.
func (s *Sequence) NextSlot() int {
	used := map[int]bool{}
	for k := range s.Accounts {
		if n, err := strconv.Atoi(k); err == nil {
			used[n] = true
		}
	}
	for n := 1; ; n++ {
		if !used[n] {
			return n
		}
	}
}

// Get returns the account in a slot, or nil.
func (s *Sequence) Get(slot int) *Account {
	return s.Accounts[strconv.Itoa(slot)]
}

// Active returns the active slot and account, or (0, nil).
func (s *Sequence) Active() (int, *Account) {
	if s.ActiveAccountNumber == nil {
		return 0, nil
	}
	return *s.ActiveAccountNumber, s.Get(*s.ActiveAccountNumber)
}

// Resolve maps a user-supplied selector (slot number, alias, or email) to a
// slot number.
func (s *Sequence) Resolve(selector string) (int, error) {
	if n, err := strconv.Atoi(selector); err == nil {
		if s.Get(n) != nil {
			return n, nil
		}
		return 0, fmt.Errorf("no account in slot %d", n)
	}
	for k, a := range s.Accounts {
		if a.Alias == selector || a.Email == selector {
			n, _ := strconv.Atoi(k)
			return n, nil
		}
	}
	return 0, fmt.Errorf("no account matching %q (try a slot number, alias, or email)", selector)
}

// FindByIdentity returns the slot of the account matching (email, orgUUID),
// or 0. Identity matching uses both fields: the same email can exist in two
// organizations.
func (s *Sequence) FindByIdentity(email, orgUUID string) int {
	for k, a := range s.Accounts {
		if a.Email == email && a.OrganizationUUID == orgUUID {
			n, _ := strconv.Atoi(k)
			return n
		}
	}
	return 0
}

// Upsert inserts or replaces an account in a slot and keeps Order in sync.
func (s *Sequence) Upsert(slot int, a *Account) {
	key := strconv.Itoa(slot)
	if _, exists := s.Accounts[key]; !exists {
		s.Order = append(s.Order, slot)
	}
	s.Accounts[key] = a
}

// Remove deletes a slot from the roster.
func (s *Sequence) Remove(slot int) {
	key := strconv.Itoa(slot)
	delete(s.Accounts, key)
	out := s.Order[:0]
	for _, n := range s.Order {
		if n != slot {
			out = append(out, n)
		}
	}
	s.Order = out
	if s.ActiveAccountNumber != nil && *s.ActiveAccountNumber == slot {
		s.ActiveAccountNumber = nil
	}
}
