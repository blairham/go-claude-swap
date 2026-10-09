// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package switcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blairham/go-claude-swap/internal/mappings"
)

func TestRemovePrunesMappingsForIdentity(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	login(t, home, "c@d.co", "", credJSON("at-c", "rt-c"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	dirA := filepath.Join(home, "proj-a")
	dirC := filepath.Join(home, "proj-c")
	os.MkdirAll(dirA, 0o700)
	os.MkdirAll(dirC, 0o700)
	store := mappings.Open()
	store.Set(dirA, "a@b.co", "")
	store.Set(dirC, "c@d.co", "")

	r, err := RemoveAccount("a@b.co")
	if err != nil {
		t.Fatal(err)
	}
	if r.PrunedMappings != 1 || r.Warning != "" {
		t.Fatalf("Removed = %+v", r)
	}
	all, _ := store.All()
	if len(all) != 1 {
		t.Fatalf("mappings after remove = %v", all)
	}
	if slot, email, _ := SlotForDirectory(dirC); slot != 2 || email != "c@d.co" {
		t.Fatalf("SlotForDirectory(c) = %d %s", slot, email)
	}
}

func TestSlotForDirectory(t *testing.T) {
	home := env(t)
	login(t, home, "a@b.co", "org-1", credJSON("at-a", "rt-a"), nil)
	if _, _, err := Add(0, ""); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	sub := filepath.Join(repo, "src")
	os.MkdirAll(sub, 0o700)

	if slot, email, err := SlotForDirectory(sub); slot != 0 || email != "" || err != nil {
		t.Fatalf("unmapped = %d %q %v", slot, email, err)
	}
	mappings.Open().Set(repo, "a@b.co", "org-1")
	if slot, email, _ := SlotForDirectory(sub); slot != 1 || email != "a@b.co" {
		t.Fatalf("mapped = %d %q", slot, email)
	}
	// A mapping to an identity no longer in the roster reports the email
	// with no slot, so the caller can say "account removed".
	mappings.Open().Set(repo, "gone@b.co", "")
	if slot, email, _ := SlotForDirectory(sub); slot != 0 || email != "gone@b.co" {
		t.Fatalf("orphaned = %d %q", slot, email)
	}
}
