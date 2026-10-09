package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readObject(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func mcpProfile(t *testing.T, profileMCP map[string]any) (home, dir string) {
	t.Helper()
	home = env(t)
	dir = Dir(1, "a@b.co")
	os.MkdirAll(dir, 0o700)
	cfg := map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@b.co"}, "theme": "light"}
	if profileMCP != nil {
		cfg["mcpServers"] = profileMCP
	}
	writeJSON(t, filepath.Join(dir, configFile), cfg)
	return home, dir
}

// TestMCPMirror: the default profile's user-scope servers are mirrored,
// definitions only the profile had are stashed once, the rest of the
// profile's config survives, and later edits (including deletions) follow.
func TestMCPMirror(t *testing.T) {
	home, dir := mcpProfile(t, map[string]any{
		"shared": map[string]any{"command": "srv"},
		"local":  map[string]any{"command": "mine"},
	})
	// A session config elsewhere must not be the source.
	other := t.TempDir()
	writeJSON(t, filepath.Join(other, ".claude.json"), map[string]any{"mcpServers": map[string]any{"wrong": map[string]any{}}})
	t.Setenv("CLAUDE_CONFIG_DIR", other)
	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{
		"mcpServers": map[string]any{"shared": map[string]any{"command": "srv"}, "new": map[string]any{"url": "https://x"}},
		"projects":   map[string]any{"/p": map[string]any{"mcpServers": map[string]any{"proj": map[string]any{}}}},
	})

	notes := syncMCPServers(dir, true)
	cfg := readObject(t, filepath.Join(dir, configFile))
	got, _ := json.Marshal(cfg["mcpServers"])
	if string(got) != `{"new":{"url":"https://x"},"shared":{"command":"srv"}}` {
		t.Fatalf("mirrored = %s", got)
	}
	if cfg["theme"] != "light" || cfg["oauthAccount"] == nil || cfg["projects"] != nil {
		t.Fatalf("profile config not preserved or project scope leaked: %v", cfg)
	}
	if !exists(filepath.Join(dir, MCPMirrorMarker)) {
		t.Fatal("adoption marker not written")
	}
	stash := readObject(t, filepath.Join(dir, MCPDisplacedStash))
	if s, _ := json.Marshal(stash); string(s) != `{"mcpServers":{"local":{"command":"mine"}},"schemaVersion":1}` {
		t.Fatalf("stash = %s", s)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], MCPDisplacedStash) {
		t.Fatalf("notes = %v", notes)
	}

	// A deletion upstream propagates; the stash is write-once.
	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{"mcpServers": map[string]any{"new": map[string]any{"url": "https://x"}}})
	syncMCPServers(dir, true)
	got, _ = json.Marshal(readObject(t, filepath.Join(dir, configFile))["mcpServers"])
	if string(got) != `{"new":{"url":"https://x"}}` {
		t.Fatalf("after upstream delete = %s", got)
	}
	if s, _ := json.Marshal(readObject(t, filepath.Join(dir, MCPDisplacedStash))); !strings.Contains(string(s), "mine") {
		t.Fatalf("stash rewritten: %s", s)
	}

	// --no-share removes the mirrored key from an adopted profile.
	syncMCPServers(dir, false)
	if _, ok := readObject(t, filepath.Join(dir, configFile))["mcpServers"]; ok {
		t.Fatal("--no-share kept the mirrored servers")
	}
}

// TestMCPMirrorLeavesUnadoptedProfileAlone: --no-share never touches a
// profile that was never mirrored, and an unusable source changes nothing.
func TestMCPMirrorLeavesUnadoptedProfileAlone(t *testing.T) {
	home, dir := mcpProfile(t, map[string]any{"local": map[string]any{"command": "mine"}})
	before, _ := os.ReadFile(filepath.Join(dir, configFile))

	syncMCPServers(dir, false)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{corrupt"), 0o600)
	syncMCPServers(dir, true)
	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{"mcpServers": nil})
	syncMCPServers(dir, true)

	if after, _ := os.ReadFile(filepath.Join(dir, configFile)); string(after) != string(before) {
		t.Fatalf("profile changed: %s", after)
	}
	if exists(filepath.Join(dir, MCPMirrorMarker)) || exists(filepath.Join(dir, MCPDisplacedStash)) {
		t.Fatal("marker or stash written without a mirror")
	}
}

// TestMCPMirrorNeverDestroysTheOnlyCopy: when the displaced definitions
// cannot be stashed (something that is not a stash holds the name), the
// profile keeps them.
func TestMCPMirrorNeverDestroysTheOnlyCopy(t *testing.T) {
	home, dir := mcpProfile(t, map[string]any{"local": map[string]any{"command": "mine"}})
	os.Mkdir(filepath.Join(dir, MCPDisplacedStash), 0o700)
	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{"mcpServers": map[string]any{}})

	notes := syncMCPServers(dir, true)
	if _, ok := readObject(t, filepath.Join(dir, configFile))["mcpServers"].(map[string]any)["local"]; !ok {
		t.Fatal("displaced definitions destroyed without a stash")
	}
	if exists(filepath.Join(dir, MCPMirrorMarker)) || len(notes) != 1 || !strings.Contains(notes[0], "not a valid stash") {
		t.Fatalf("marker written or no note: %v", notes)
	}
}

// TestMCPMirrorAdoptsInSyncProfile: a profile already matching the source
// is adopted without a stash.
func TestMCPMirrorAdoptsInSyncProfile(t *testing.T) {
	home, dir := mcpProfile(t, map[string]any{"s": map[string]any{"command": "x"}})
	writeJSON(t, filepath.Join(home, ".claude.json"), map[string]any{"mcpServers": map[string]any{"s": map[string]any{"command": "x"}}})
	if notes := syncMCPServers(dir, true); len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	if !exists(filepath.Join(dir, MCPMirrorMarker)) || exists(filepath.Join(dir, MCPDisplacedStash)) {
		t.Fatal("in-sync profile not adopted cleanly")
	}
}
