// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package account

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// FuzzRoster feeds arbitrary sequence.json content to Load. sequence.json is
// shared with the Python claude-swap and hand-editable, so cswap must not
// crash on it: a roster Load accepts must resolve every selector it holds
// without panicking, and a Save must write back the same roster (only
// lastUpdated and the slot order's sorting may change).
func FuzzRoster(f *testing.F) {
	f.Add([]byte(`{"activeAccountNumber":1,"lastUpdated":"2026-08-11T00:00:00Z","sequence":[1,3],` +
		`"accounts":{"1":{"email":"a@b.c","uuid":"u1","organizationUuid":"","organizationName":"",` +
		`"added":"2026-08-11T00:00:00Z"},"3":{"email":"x@y.z","alias":"work","kind":"api_key","disabled":true}}}`))
	f.Add([]byte(`{"activeAccountNumber":7,"sequence":[],"accounts":{}}`))
	f.Add([]byte(`{"accounts":{"1":null}}`))
	f.Add([]byte(`{"accounts":{"x":{"email":"1"}}}`))
	f.Add([]byte(`{"activeAccountNumber":null,"accounts":null}`))
	f.Add([]byte(`[]`))
	root := f.TempDir()
	f.Setenv("HOME", root)
	f.Setenv("XDG_DATA_HOME", filepath.Join(root, "share"))
	if err := os.MkdirAll(filepath.Dir(paths.SequencePath()), 0o700); err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, doc []byte) {
		if err := os.WriteFile(paths.SequencePath(), doc, 0o600); err != nil {
			t.Fatal(err)
		}
		seq, err := Load()
		if err != nil {
			return
		}
		if seq.Accounts == nil {
			t.Fatal("Load returned a nil account map")
		}

		// Every selector the roster itself names must resolve, to a slot
		// whose account carries it.
		seq.Active()
		seq.NextSlot()
		for key, a := range seq.Accounts {
			if a == nil {
				t.Fatalf("Load kept a null account in slot %q", key)
			}
			slot, convErr := strconv.Atoi(key)
			if convErr != nil {
				continue // not a slot: unreachable by any selector
			}
			if strconv.Itoa(slot) == key {
				if got, resErr := seq.Resolve(key); resErr != nil || got != slot {
					t.Fatalf("Resolve(%q) = %d, %v; want slot %d", key, got, resErr, slot)
				}
			}
			for _, sel := range []string{a.Alias, a.Email} {
				// A numeric selector is a slot number first, which is why
				// ValidateAlias refuses numeric aliases.
				if _, numeric := strconv.Atoi(sel); sel == "" || numeric == nil {
					continue
				}
				n, resErr := seq.Resolve(sel)
				if resErr != nil {
					t.Fatalf("Resolve(%q) for the account in slot %q: %v", sel, key, resErr)
				}
				if b := seq.Get(n); b == nil || (b.Alias != sel && b.Email != sel) {
					t.Fatalf("Resolve(%q) = slot %d, which does not carry it", sel, n)
				}
			}
		}

		want := *seq
		want.Order = append([]int(nil), seq.Order...)
		sort.Ints(want.Order)
		if saveErr := seq.Save(); saveErr != nil {
			t.Fatalf("Save of a loaded roster: %v", saveErr)
		}
		back, err := Load()
		if err != nil {
			t.Fatalf("Load after Save: %v", err)
		}
		back.LastUpdated, want.LastUpdated = "", ""
		if len(back.Order) == 0 && len(want.Order) == 0 {
			back.Order, want.Order = nil, nil
		}
		if !reflect.DeepEqual(back, &want) {
			t.Fatalf("Save changed the roster\nloaded: %+v\nsaved:  %+v", &want, back)
		}
	})
}
