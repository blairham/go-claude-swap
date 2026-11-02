// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package account

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blairham/go-claude-swap/internal/paths"
)

func withHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "share"))
	return dir
}

func TestLoadMissingIsEmpty(t *testing.T) {
	withHome(t)
	seq, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(seq.Accounts) != 0 || seq.ActiveAccountNumber != nil {
		t.Errorf("expected empty roster, got %+v", seq)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withHome(t)
	seq, _ := Load()
	seq.Upsert(1, &Account{Email: "a@b.c", Added: "2026-08-11T00:00:00Z"})
	seq.Upsert(3, &Account{Email: "x@y.z", Alias: "work", OrganizationUUID: "org1"})
	one := 1
	seq.ActiveAccountNumber = &one
	if err := seq.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Get(1).Email != "a@b.c" || got.Get(3).Alias != "work" {
		t.Errorf("round trip lost data: %+v", got)
	}
	if len(got.Order) != 2 || got.Order[0] != 1 || got.Order[1] != 3 {
		t.Errorf("order = %v", got.Order)
	}
	if got.ActiveAccountNumber == nil || *got.ActiveAccountNumber != 1 {
		t.Error("active pointer lost")
	}
}

func TestCorruptRosterIsError(t *testing.T) {
	dir := withHome(t)
	// darwin backup root is ~/.claude-swap-backup
	root := filepath.Join(dir, ".claude-swap-backup")
	os.MkdirAll(root, 0o700)
	os.WriteFile(filepath.Join(root, "sequence.json"), []byte("{torn"), 0o600)
	if _, err := Load(); err == nil {
		// On linux the root is XDG-based; write there too and re-check.
		xdgRoot := filepath.Join(dir, "share", "claude-swap")
		os.MkdirAll(xdgRoot, 0o700)
		os.WriteFile(filepath.Join(xdgRoot, "sequence.json"), []byte("{torn"), 0o600)
		if _, err := Load(); err == nil {
			t.Fatal("a torn roster must never be read as empty")
		}
	}
}

func TestResolve(t *testing.T) {
	withHome(t)
	seq, _ := Load()
	seq.Upsert(1, &Account{Email: "a@b.c", Alias: "dev"})
	seq.Upsert(2, &Account{Email: "x@y.z"})

	for selector, want := range map[string]int{"1": 1, "dev": 1, "x@y.z": 2} {
		got, err := seq.Resolve(selector)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %d, %v; want %d", selector, got, err, want)
		}
	}
	if _, err := seq.Resolve("9"); err == nil {
		t.Error("empty slot should not resolve")
	}
	if _, err := seq.Resolve("nobody@nowhere"); err == nil {
		t.Error("unknown email should not resolve")
	}
}

// A hand-edited key that parses as a number but is not the form Get looks up
// ("01", "+2", "00") is not a slot. Reading it as one handed callers another
// account's slot: with "1" holding x and "01" holding y, removing y removed
// slot 1 and x's backup, and capturing y again rewrote slot 1's x as y.
func TestNonCanonicalKeyIsNotASlot(t *testing.T) {
	seq := &Sequence{Accounts: map[string]*Account{
		"1":  {Email: "x@y.z"},
		"01": {Email: "y@y.z", Alias: "padded"},
		"+2": {Email: "plus@y.z"},
		"00": {Email: "zero@y.z"},
	}}
	for _, sel := range []string{"y@y.z", "padded", "plus@y.z", "zero@y.z"} {
		if n, err := seq.Resolve(sel); err == nil {
			t.Errorf("Resolve(%q) = slot %d, which holds %+v; want no match", sel, n, seq.Get(n))
		}
	}
	for _, email := range []string{"y@y.z", "plus@y.z", "zero@y.z"} {
		if n := seq.FindByIdentity(email, ""); n != 0 {
			t.Errorf("FindByIdentity(%q) = slot %d, which holds %+v; want 0", email, n, seq.Get(n))
		}
	}
	if n, err := seq.Resolve("x@y.z"); err != nil || n != 1 {
		t.Errorf("Resolve(x@y.z) = %d, %v; want 1", n, err)
	}
	if n := seq.FindByIdentity("x@y.z", ""); n != 1 {
		t.Errorf("FindByIdentity(x@y.z) = %d; want 1", n)
	}
	// NextSlot stays wary of them: no new slot lands beside a key that
	// reads as the same number.
	if got := seq.NextSlot(); got != 3 {
		t.Errorf("NextSlot = %d, want 3", got)
	}
}

func TestNextSlotFillsGaps(t *testing.T) {
	withHome(t)
	seq, _ := Load()
	seq.Upsert(1, &Account{Email: "a@b.c"})
	seq.Upsert(3, &Account{Email: "c@d.e"})
	if got := seq.NextSlot(); got != 2 {
		t.Errorf("NextSlot = %d, want 2", got)
	}
}

func TestValidateAlias(t *testing.T) {
	for _, ok := range []string{"work", "dev-2", "a.b_c"} {
		if err := ValidateAlias(ok); err != nil {
			t.Errorf("ValidateAlias(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "UPPER", "123", "-lead", "has space"} {
		if err := ValidateAlias(bad); err == nil {
			t.Errorf("ValidateAlias(%q) should fail", bad)
		}
	}
}

func TestRemoveClearsActivePointer(t *testing.T) {
	withHome(t)
	seq, _ := Load()
	seq.Upsert(1, &Account{Email: "a@b.c"})
	one := 1
	seq.ActiveAccountNumber = &one
	seq.Remove(1)
	if seq.ActiveAccountNumber != nil || len(seq.Order) != 0 {
		t.Errorf("remove left state: %+v", seq)
	}
}

// TestKindSurvivesRewrite: claude-swap marks API-key rows with "kind";
// a roster rewrite here must not drop it, or the original would start
// treating the API key as an OAuth account.
func TestKindSurvivesRewrite(t *testing.T) {
	withHome(t)
	os.MkdirAll(filepath.Dir(paths.SequencePath()), 0o700)
	py := `{
  "activeAccountNumber": null,
  "lastUpdated": "2026-01-01T00:00:00Z",
  "sequence": [1],
  "accounts": {
    "1": {"email": "api-key-1@token.local", "uuid": "", "organizationUuid": "", "organizationName": "", "added": "2026-01-01T00:00:00Z", "kind": "api_key"}
  }
}`
	if err := os.WriteFile(paths.SequencePath(), []byte(py), 0o600); err != nil {
		t.Fatal(err)
	}
	seq, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if seq.Get(1).Kind != KindAPIKey {
		t.Fatalf("kind not read: %+v", seq.Get(1))
	}
	if err := seq.Save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(paths.SequencePath())
	if !strings.Contains(string(raw), `"kind": "api_key"`) {
		t.Fatalf("kind dropped on rewrite: %s", raw)
	}
}
