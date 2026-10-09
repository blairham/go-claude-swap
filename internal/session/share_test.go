// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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

	notes := SyncSharing(dir, true, false)
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
	SyncSharing(dir, false, false)
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

	SyncSharing(dir, true, false)
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

// TestShareHistoryMergesThenLinks: a profile's own history is merged into
// ~/.claude (duplicate transcripts dropped, prompt lines deduplicated), then
// linked; turning the flag off unlinks without touching ~/.claude.
func TestShareHistoryMergesThenLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink mode")
	}
	home := env(t)
	src := filepath.Join(home, ".claude")
	os.MkdirAll(filepath.Join(src, "projects", "p1"), 0o700)
	os.WriteFile(filepath.Join(src, "projects", "p1", "a.jsonl"), []byte("home\n"), 0o600)
	os.WriteFile(filepath.Join(src, "history.jsonl"), []byte("x\n"), 0o600)

	dir := Dir(1, "a@b.co")
	os.MkdirAll(filepath.Join(dir, "projects", "p1"), 0o700)
	os.MkdirAll(filepath.Join(dir, "projects", "p2"), 0o700)
	os.WriteFile(filepath.Join(dir, "projects", "p1", "a.jsonl"), []byte("dup\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "projects", "p2", "b.jsonl"), []byte("profile\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte("x\ny\n"), 0o600)

	notes := SyncSharing(dir, false, true)
	if len(notes) != 2 {
		t.Fatalf("notes = %v", notes)
	}
	for _, name := range []string{"projects", "history.jsonl"} {
		want, _ := filepath.EvalSymlinks(filepath.Join(src, name))
		if target, err := os.Readlink(filepath.Join(dir, name)); err != nil || target != want {
			t.Fatalf("%s not linked: %q %v", name, target, err)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(src, "projects", "p2", "b.jsonl")); string(raw) != "profile\n" {
		t.Fatalf("profile transcript not merged: %q", raw)
	}
	if raw, _ := os.ReadFile(filepath.Join(src, "projects", "p1", "a.jsonl")); string(raw) != "home\n" {
		t.Fatalf("existing transcript overwritten: %q", raw)
	}
	if raw, _ := os.ReadFile(filepath.Join(src, "history.jsonl")); string(raw) != "x\ny\n" {
		t.Fatalf("history.jsonl = %q", raw)
	}
	if _, err := os.Lstat(filepath.Join(dir, "settings.json")); err == nil {
		t.Fatal("--no-share still shared customizations")
	}
	var m manifest
	raw, _ := os.ReadFile(filepath.Join(dir, ShareManifest))
	if json.Unmarshal(raw, &m) != nil || len(m.Items) != 2 {
		t.Fatalf("manifest = %s", raw)
	}

	SyncSharing(dir, true, false)
	if _, err := os.Lstat(filepath.Join(dir, "projects")); err == nil {
		t.Fatal("history link kept after --share-history was dropped")
	}
	if raw, _ := os.ReadFile(filepath.Join(src, "history.jsonl")); string(raw) != "x\ny\n" {
		t.Fatal("unsharing touched the shared history")
	}
}

// TestShareHistoryWaitsForQuiescence: real history is not moved out from
// under a Claude Code running in the profile.
func TestShareHistoryWaitsForQuiescence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink mode")
	}
	env(t)
	dir := Dir(1, "a@b.co")
	os.MkdirAll(filepath.Join(dir, "sessions"), 0o700)
	os.WriteFile(filepath.Join(dir, "sessions", "1.json"), []byte(fmt.Sprintf(`{"pid":%d}`, os.Getpid())), 0o600)
	os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte("mine\n"), 0o600)

	notes := SyncSharing(dir, false, true)
	if raw, _ := os.ReadFile(filepath.Join(dir, "history.jsonl")); string(raw) != "mine\n" {
		t.Fatal("history moved under a live session")
	}
	if !slices.ContainsFunc(
		notes,
		func(n string) bool { return strings.Contains(n, "another session is using this profile") },
	) {
		t.Fatalf("notes = %v", notes)
	}
	// projects/ had nothing to merge, so it is linked (and created) anyway.
	if fi, err := os.Stat(
		filepath.Join(os.Getenv("HOME"), ".claude", "projects"),
	); err != nil ||
		fi.Mode().Perm() != 0o700 {
		t.Fatalf("share source not created private: %v %v", fi, err)
	}
	if !isSymlink(filepath.Join(dir, "projects")) {
		t.Fatal("projects not linked")
	}
}
