package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSyncSharing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink mode")
	}
	home := env(t)
	src := filepath.Join(home, ".claude")
	os.MkdirAll(filepath.Join(src, "skills"), 0o700)
	os.WriteFile(filepath.Join(src, "settings.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(src, "CLAUDE.md"), []byte("# mine"), 0o600)

	dir := Dir(1, "a@b.co")
	os.MkdirAll(dir, 0o700)
	// The profile's own CLAUDE.md is user data and must survive.
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# profile"), 0o600)

	notes := SyncSharing(dir, true)
	if len(notes) != 1 {
		t.Fatalf("notes = %v", notes)
	}
	for _, name := range []string{"settings.json", "skills"} {
		if target, err := os.Readlink(filepath.Join(dir, name)); err != nil || filepath.Base(target) != name {
			t.Fatalf("%s not linked: %q %v", name, target, err)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(raw) != "# profile" {
		t.Fatal("pre-existing profile file replaced")
	}
	var m manifest
	raw, _ := os.ReadFile(filepath.Join(dir, ShareManifest))
	if json.Unmarshal(raw, &m) != nil || m.Mode != "symlink" || len(m.Items) != 2 {
		t.Fatalf("manifest = %s", raw)
	}

	// --no-share removes only what was linked.
	SyncSharing(dir, false)
	if _, err := os.Lstat(filepath.Join(dir, "settings.json")); err == nil {
		t.Fatal("managed link survived --no-share")
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Fatal("--no-share removed user data")
	}
	if _, err := os.Stat(filepath.Join(src, "settings.json")); err != nil {
		t.Fatal("--no-share removed the source")
	}
	if _, err := os.Stat(filepath.Join(dir, ShareManifest)); err == nil {
		t.Fatal("manifest left behind")
	}
}

// TestSyncSharingHonoursOriginalManifest: a profile whose history the
// original linked with --share-history is unlinked (as the original does
// without the flag), and real history is never deleted.
func TestSyncSharingHonoursOriginalManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink mode")
	}
	home := env(t)
	src := filepath.Join(home, ".claude")
	os.MkdirAll(filepath.Join(src, "projects"), 0o700)
	dir := Dir(1, "a@b.co")
	os.MkdirAll(dir, 0o700)
	os.Symlink(filepath.Join(src, "projects"), filepath.Join(dir, "projects"))
	os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte("real\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ShareManifest), []byte(`{
  "items": [
    "projects",
    "history.jsonl"
  ],
  "mode": "symlink"
}`), 0o600)

	SyncSharing(dir, true)
	if _, err := os.Lstat(filepath.Join(dir, "projects")); err == nil {
		t.Fatal("history link kept without --share-history")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "history.jsonl")); string(raw) != "real\n" {
		t.Fatal("real history deleted on the manifest's word")
	}
	if _, err := os.Stat(filepath.Join(src, "projects")); err != nil {
		t.Fatal("shared history source removed")
	}
}
