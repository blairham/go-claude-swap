package account

import (
	"os"
	"path/filepath"
	"testing"
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
