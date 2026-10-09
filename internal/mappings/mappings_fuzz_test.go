// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package mappings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unicode/utf8"
)

// FuzzCovers checks the ancestor test `cswap run` resolves a directory's
// account with against its definition: for clean absolute paths, dir covers
// target exactly when target is dir or walking target's parents reaches dir.
// A plain string-prefix test gets this wrong for /foo/bar vs /foo/barbaz.
func FuzzCovers(f *testing.F) {
	f.Add("foo/bar", "foo/barbaz")
	f.Add("foo/bar", "foo/bar/baz")
	f.Add("", "anything")
	f.Add("a", "a")
	f.Add("a/b", "a")
	f.Fuzz(func(t *testing.T, dir, target string) {
		root := string(filepath.Separator)
		d := filepath.Clean(root + dir)
		tg := filepath.Clean(root + target)

		want := false
		for p := tg; ; p = filepath.Dir(p) {
			if p == d {
				want = true
				break
			}
			if p == filepath.Dir(p) {
				break
			}
		}
		if got := covers(d, tg); got != want {
			t.Fatalf("covers(%q, %q) = %v, want %v", d, tg, got, want)
		}
	})
}

// FuzzMappingsFile feeds arbitrary mappings.json content to the store.
// mappings.json is shared with the Python claude-swap, which may add fields
// cswap does not know, so a write must carry every other mapping through
// unchanged — every field, known or not — and a file the store cannot parse
// must be refused rather than overwritten.
func FuzzMappingsFile(f *testing.F) {
	f.Add([]byte(`{"schemaVersion":1,"mappings":{"/work":{"email":"a@b.c","organizationUuid":"o",`+
		`"added":"2026-08-11T00:00:00Z","note":"<kept>"}}}`), "/work/repo")
	f.Add([]byte(`{"mappings":{"/x":null,"/y":{"email":7}}}`), "/y")
	f.Add([]byte(`{"mappings":null}`), "~")
	f.Add([]byte(`{"schemaVersion":"1"}`), "rel/dir")
	f.Add([]byte(`garbage`), "/")
	f.Fuzz(func(t *testing.T, doc []byte, dir string) {
		path := filepath.Join(t.TempDir(), "mappings.json")
		if err := os.WriteFile(path, doc, 0o600); err != nil {
			t.Fatal(err)
		}
		s := &Store{path: path}
		before, err := s.load()
		if err != nil {
			if _, _, serr := s.Set("/elsewhere", "e", "o"); serr == nil {
				t.Fatal("Set replaced a mappings file load refused")
			}
			if after, _ := os.ReadFile(path); string(after) != string(doc) {
				t.Fatal("a refused mappings file was modified")
			}
			return
		}
		if _, allErr := s.All(); allErr != nil {
			t.Fatalf("All after a successful load: %v", allErr)
		}
		if _, _, _, resErr := s.Resolve(dir); resErr != nil {
			return // dir is not a usable path (NUL, unresolvable ~)
		}

		key, _, err := s.Set(dir, "new@example.com", "org")
		if err != nil {
			if norm, nerr := NormalizePath(dir); nerr == nil && !utf8.ValidString(norm) {
				return // refused: JSON cannot hold the key
			}
			t.Fatalf("Set(%q): %v", dir, err)
		}
		after, err := s.load()
		if err != nil {
			t.Fatalf("load after Set: %v", err)
		}
		if got := entryOf(after[key]); got.Email != "new@example.com" || got.OrganizationUUID != "org" {
			t.Fatalf("Set(%q) stored %+v under %q", dir, got, key)
		}
		for k, fields := range before {
			if k == key {
				continue
			}
			if !reflect.DeepEqual(decode(t, fields), decode(t, after[k])) {
				t.Fatalf("Set(%q) changed the unrelated mapping %q\nbefore: %s\nafter:  %s",
					dir, k, mustJSON(fields), mustJSON(after[k]))
			}
		}
	})
}

func decode(t *testing.T, fields map[string]json.RawMessage) map[string]any {
	t.Helper()
	if fields == nil {
		return nil
	}
	out := make(map[string]any, len(fields))
	for k, raw := range fields {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("field %q: %v", k, err)
		}
		out[k] = v
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
