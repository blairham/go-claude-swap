package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blairham/go-claude-swap/internal/paths"
)

func withBackupRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// BackupRoot derives from HOME on darwin.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "share"))
	return dir
}

func TestLoadDefaultsWhenMissing(t *testing.T) {
	withBackupRoot(t)
	s := Load()
	if s.Float("autoswitch.threshold") != 90.0 {
		t.Errorf("threshold default = %v", s.Float("autoswitch.threshold"))
	}
	if s.String("ui.theme") != "auto" {
		t.Errorf("theme default = %v", s.Get("ui.theme"))
	}
	if s.IsSet("autoswitch.threshold") {
		t.Error("nothing should be marked set")
	}
}

func TestSetGetUnsetRoundTrip(t *testing.T) {
	withBackupRoot(t)
	v, err := ParseStrict("autoswitch.threshold", "85.5")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetKey("autoswitch.threshold", v); err != nil {
		t.Fatal(err)
	}
	s := Load()
	if s.Float("autoswitch.threshold") != 85.5 || !s.IsSet("autoswitch.threshold") {
		t.Errorf("got %v set=%v", s.Get("autoswitch.threshold"), s.IsSet("autoswitch.threshold"))
	}
	removed, err := UnsetKey("autoswitch.threshold")
	if err != nil || !removed {
		t.Fatalf("unset: %v %v", removed, err)
	}
	if Load().IsSet("autoswitch.threshold") {
		t.Error("still set after unset")
	}
}

func TestParseStrictValidation(t *testing.T) {
	for _, bad := range []struct{ key, val string }{
		{"autoswitch.threshold", "101"},
		{"autoswitch.threshold", "49"},
		{"autoswitch.threshold", "abc"},
		{"autoswitch.strategy", "wrong"},
		{"ui.theme", "neon"},
		{"autoswitch.unhealthyTicks", "0"},
		{"nope.nope", "1"},
	} {
		if _, err := ParseStrict(bad.key, bad.val); err == nil {
			t.Errorf("ParseStrict(%s, %s) should fail", bad.key, bad.val)
		}
	}
	if v, err := ParseStrict("autoswitch.includeApiKeyAccounts", "YES"); err != nil || v != true {
		t.Errorf("bool yes: %v %v", v, err)
	}
}

func TestLoadClampsOutOfRange(t *testing.T) {
	withBackupRoot(t)
	// Write threshold above the cap directly.
	os.MkdirAll(filepath.Dir(paths.SettingsPath()), 0o700)
	os.WriteFile(paths.SettingsPath(),
		[]byte(`{"schemaVersion":1,"autoswitch":{"threshold":150}}`), 0o600)
	s := Load()
	if got := s.Float("autoswitch.threshold"); got != 99.9 {
		t.Errorf("clamped threshold = %v, want 99.9", got)
	}
}

func TestParseModelNames(t *testing.T) {
	got := ParseModelNames("Fable, Opus,fable , ,Opus")
	if len(got) != 2 || got[0] != "Fable" || got[1] != "Opus" {
		t.Errorf("ParseModelNames = %v", got)
	}
	if ParseModelNames("") != nil {
		t.Error("empty → nil")
	}
}
