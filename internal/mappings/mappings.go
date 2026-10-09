// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package mappings stores directory → account mappings: a normalized
// absolute directory path keyed to a stored account identity, so that
// `cswap run` with no account argument can launch the account mapped to the
// current directory (or its nearest mapped ancestor).
//
// The file is <backup root>/mappings.json in claude-swap's exact format:
//
//	{"schemaVersion": 1, "mappings": {"/abs/dir": {"email": ..., "organizationUuid": ..., "added": ...}}}
//
// Identity is the stable (email, organizationUuid) composite rather than the
// slot number, because slot numbers are reused when accounts are removed and
// re-added. Callers resolve an entry to a live slot through the roster.
package mappings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// SchemaVersion of mappings.json.
const SchemaVersion = 1

// Entry is one mapping's target identity.
type Entry struct {
	Email            string
	OrganizationUUID string
	Added            string
}

// document mirrors the on-disk shape. Entries stay as raw field maps so a
// rewrite round-trips any field a newer claude-swap adds.
type document struct {
	SchemaVersion int                                   `json:"schemaVersion"`
	Mappings      map[string]map[string]json.RawMessage `json:"mappings"`
}

// Store reads and writes mappings.json.
type Store struct {
	path string
}

// Open returns the store at the default location under the backup root.
func Open() *Store { return &Store{path: paths.MappingsPath()} }

// NormalizePath turns a user-supplied directory into its mapping key:
// "~" expanded, absolute, symlinks resolved as far as the path exists, and
// case-folded on Windows — so one directory always yields one key however it
// was typed. Matches claude-swap's Path.expanduser().resolve() + normcase.
func NormalizePath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(h, p[1:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	resolved := resolveExisting(abs)
	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
	}
	return resolved, nil
}

// resolveExisting resolves symlinks in the longest existing prefix of an
// absolute path and re-appends the rest (non-strict resolve).
func resolveExisting(abs string) string {
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs
	}
	return filepath.Join(resolveExisting(parent), filepath.Base(abs))
}

// load reads the file. A missing file is an empty table; a present but
// unreadable or unparseable one is an error, so a write never silently
// replaces mappings it could not read.
func (s *Store) load() (map[string]map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.path, err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("corrupt %s (fix or delete it): %w", s.path, err)
	}
	if doc.Mappings == nil {
		doc.Mappings = map[string]map[string]json.RawMessage{}
	}
	return doc.Mappings, nil
}

func entryOf(fields map[string]json.RawMessage) Entry {
	str := func(k string) string {
		var v string
		if raw, ok := fields[k]; ok {
			_ = json.Unmarshal(raw, &v) // null or a non-string reads as ""
		}
		return v
	}
	return Entry{Email: str("email"), OrganizationUUID: str("organizationUuid"), Added: str("added")}
}

func (s *Store) write(m map[string]map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(document{SchemaVersion: SchemaVersion, Mappings: m}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	return account.WriteFileAtomic(s.path, data, 0o600)
}

// All returns every mapping, keyed by normalized directory.
func (s *Store) All() (map[string]Entry, error) {
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Entry, len(m))
	for k, f := range m {
		out[k] = entryOf(f)
	}
	return out, nil
}

// Get is an exact-match lookup (no ancestor walk); nil when unmapped.
func (s *Store) Get(dir string) (*Entry, error) {
	key, err := NormalizePath(dir)
	if err != nil {
		return nil, err
	}
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	f, ok := m[key]
	if !ok {
		return nil, nil
	}
	e := entryOf(f)
	return &e, nil
}

// Set maps dir to (email, orgUUID), replacing any previous mapping for it.
// Returns the normalized key and the previous entry (nil if none).
func (s *Store) Set(dir, email, orgUUID string) (string, *Entry, error) {
	key, err := NormalizePath(dir)
	if err != nil {
		return "", nil, err
	}
	// JSON strings are UTF-8: a non-UTF-8 path would be stored with U+FFFD
	// in it, a key no directory ever normalizes to.
	if !utf8.ValidString(key) {
		return "", nil, fmt.Errorf("cannot map %q: mappings.json holds only UTF-8 paths", key)
	}
	m, err := s.load()
	if err != nil {
		return "", nil, err
	}
	var prev *Entry
	if f, ok := m[key]; ok {
		e := entryOf(f)
		prev = &e
	}
	enc := func(v string) json.RawMessage { b, _ := json.Marshal(v); return b }
	m[key] = map[string]json.RawMessage{
		"email":            enc(email),
		"organizationUuid": enc(orgUUID),
		"added":            enc(time.Now().UTC().Format(account.TimeFormat)),
	}
	return key, prev, s.write(m)
}

// Remove deletes the mapping for dir. Returns the normalized key and
// whether a mapping existed.
func (s *Store) Remove(dir string) (string, bool, error) {
	key, err := NormalizePath(dir)
	if err != nil {
		return "", false, err
	}
	m, err := s.load()
	if err != nil {
		return key, false, err
	}
	if _, ok := m[key]; !ok {
		return key, false, nil
	}
	delete(m, key)
	return key, true, s.write(m)
}

// PruneAccount drops every mapping that targets (email, orgUUID) and returns
// how many were removed. Called when an identity leaves the roster for good.
func (s *Store) PruneAccount(email, orgUUID string) (int, error) {
	m, err := s.load()
	if err != nil {
		return 0, err
	}
	n := 0
	for k, f := range m {
		if e := entryOf(f); e.Email == email && e.OrganizationUUID == orgUUID {
			delete(m, k)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.write(m)
}

// Resolve returns the mapping for dir or its nearest mapped ancestor (the
// longest key on the root→dir chain). ok is false when nothing covers dir.
func (s *Store) Resolve(dir string) (key string, e Entry, ok bool, err error) {
	target, err := NormalizePath(dir)
	if err != nil {
		return "", Entry{}, false, err
	}
	m, err := s.load()
	if err != nil {
		return "", Entry{}, false, err
	}
	for k, f := range m {
		if covers(k, target) && len(k) > len(key) {
			key, e, ok = k, entryOf(f), true
		}
	}
	return key, e, ok, nil
}

// covers reports whether dir equals target or is one of its ancestors. A
// plain prefix test would let /foo/bar claim /foo/barbaz.
func covers(dir, target string) bool {
	if dir == target {
		return true
	}
	prefix := dir
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(target, prefix)
}
