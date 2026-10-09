// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/blairham/go-claude-swap/internal/locks"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// The user-scope MCP mirror, shared with claude-swap.
const (
	mcpKey = "mcpServers"
	// MCPMirrorMarker records that a profile's mcpServers is (or was)
	// cswap-mirrored. It gates both the one-time displacement stash and
	// --no-share's removal of the key, so definitions a profile had before
	// mirroring existed are never silently destroyed.
	MCPMirrorMarker = ".cswap-mcp-mirror-v1"
	// MCPDisplacedStash is where the first mirror saves (write-once) the
	// session-local definitions it displaces.
	MCPDisplacedStash = ".cswap-mcp-displaced.json"
)

// syncMCPServers mirrors the default profile's user-scope mcpServers from
// ~/.claude.json into a profile's .claude.json. Pure mirror: the default
// profile is the source of truth, so adds, edits and deletions propagate,
// and MCP changes made inside a session are overwritten on the next launch.
// Nothing flows back, and per-project mcpServers are untouched on both
// sides.
//
// !share removes the mirrored key, but only from a profile that adopted
// mirroring (the marker). Fail-open throughout: an unreadable or malformed
// file on either side, a symlinked target, or a contended lock leaves the
// profile alone and never blocks the launch. The in-sync steady state takes
// no lock and writes nothing; adoption always goes through Claude Code's
// config lock for the profile, so the marker never certifies a state a
// running Claude Code changed between the read and the touch.
func syncMCPServers(dir string, share bool) []string {
	configPath := filepath.Join(dir, configFile)
	marker := filepath.Join(dir, MCPMirrorMarker)

	var source map[string]json.RawMessage
	switch {
	case share:
		src, ok := readMCPSource()
		if !ok {
			return nil
		}
		source = src
	case exists(marker):
		source = map[string]json.RawMessage{} // remove what was mirrored
	default:
		return nil // never adopted: --no-share must not touch local data
	}

	// Type-check before reading: a FIFO would hang the launch, and a
	// symlinked target must never be written through or replaced.
	if !exists(configPath) {
		return nil // bootstrap and validation own a missing config
	}
	if !isRegular(configPath) {
		return []string{fmt.Sprintf("Not syncing MCP servers: %s is not a regular file.", configPath)}
	}
	existing, ok := loadJSONObject(configPath)
	if !ok {
		return nil // bootstrap and validation own a broken config
	}
	target, ok := mcpObject(existing)
	if !ok {
		return []string{fmt.Sprintf("Not syncing MCP servers: the profile's %s is not an object.", mcpKey)}
	}
	if mcpEqual(target, source) && (!share || exists(marker)) {
		return nil
	}

	// The lock a Claude Code running in this profile takes for its own
	// .claude.json writes (its CLAUDE_CONFIG_DIR is the profile).
	lock := locks.ClaudeConfigLock(configPath)
	if err := lock.Acquire(); err != nil {
		return []string{fmt.Sprintf("Could not sync MCP servers (%v) — skipping this launch.", err)}
	}
	defer lock.Release()

	// Re-read both sides: a writer that waited here must not clobber a
	// newer mirror with its stale pre-lock snapshot.
	if share {
		if source, ok = readMCPSource(); !ok {
			return nil
		}
	}
	if !isRegular(configPath) {
		return nil
	}
	if existing, ok = loadJSONObject(configPath); !ok {
		return nil
	}
	if target, ok = mcpObject(existing); !ok {
		return nil
	}
	var notes []string
	if mcpEqual(target, source) {
		if share {
			notes = append(notes, ensureMarker(marker)...)
		}
		return notes
	}
	if share && !exists(marker) {
		displaced := map[string]json.RawMessage{}
		for name, v := range target {
			if sv, ok := source[name]; !ok || !jsonEqual(sv, v) {
				displaced[name] = v
			}
		}
		if len(displaced) > 0 {
			saved, n := stashDisplacedMCP(dir, displaced)
			notes = append(notes, n...)
			if !saved {
				return notes // never destroy the only copy
			}
		}
	}
	if len(source) > 0 {
		raw, err := json.Marshal(source)
		if err != nil {
			return notes
		}
		existing[mcpKey] = raw
	} else {
		delete(existing, mcpKey) // Claude Code strips default-valued keys too
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err == nil {
		err = writePrivate(configPath, data)
	}
	if err != nil {
		return append(notes, fmt.Sprintf("Could not sync MCP servers: %v", err))
	}
	if share {
		// Only after a successful write: a profile whose marker fails to
		// land retries next launch, by then in sync, so nothing is
		// mis-stashed.
		notes = append(notes, ensureMarker(marker)...)
	}
	return notes
}

// readMCPSource returns the default profile's user-scope mcpServers. A
// readable config without the key has none (an empty map, which propagates
// the removal); a missing or corrupt config, or a non-object value, is
// unusable (ok=false) and leaves the profile untouched.
func readMCPSource() (map[string]json.RawMessage, bool) {
	cfg, ok := loadJSONObject(paths.DefaultGlobalConfigPath())
	if !ok {
		return nil, false
	}
	return mcpObject(cfg)
}

// mcpObject extracts mcpServers from a config: absent is empty, anything
// but an object (null included) is unusable.
func mcpObject(cfg map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	raw, ok := cfg[mcpKey]
	if !ok {
		return map[string]json.RawMessage{}, true
	}
	if !isJSONObject(raw) {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

func loadJSONObject(path string) (map[string]json.RawMessage, bool) {
	raw, err := os.ReadFile(path)
	if err != nil || !isJSONObject(raw) {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// jsonEqual compares two JSON values by meaning, not spelling.
func jsonEqual(a, b json.RawMessage) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(va, vb)
}

func mcpEqual(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !jsonEqual(va, vb) {
			return false
		}
	}
	return true
}

func ensureMarker(marker string) []string {
	if exists(marker) {
		return nil
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return []string{fmt.Sprintf("Could not write %s: %v", filepath.Base(marker), err)}
	}
	_ = f.Close()
	return nil
}

// stashDisplacedMCP saves the definitions the first mirror would displace;
// false aborts the mirror. Write-once: a stash left by an interrupted
// adoption holds the original data and is kept — but only a valid stash
// counts as a saved copy; anything else squatting on the name blocks.
func stashDisplacedMCP(dir string, displaced map[string]json.RawMessage) (bool, []string) {
	stash := filepath.Join(dir, MCPDisplacedStash)
	if _, err := os.Lstat(stash); err == nil {
		if validStash(stash) {
			return true, nil
		}
		return false, []string{
			MCPDisplacedStash + " exists but is not a valid stash; leaving the profile's MCP servers in place.",
		}
	}
	payload := struct {
		SchemaVersion int                        `json:"schemaVersion"`
		MCPServers    map[string]json.RawMessage `json:"mcpServers"`
	}{1, displaced}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err == nil {
		err = writePrivate(stash, data)
	}
	if err != nil {
		return false, []string{fmt.Sprintf("Could not stash the profile's MCP servers (%v); leaving them in place.", err)}
	}
	return true, []string{
		"Session MCP servers now mirror your default profile; the profile's previous definitions were saved to " + MCPDisplacedStash + ".",
	}
}

func validStash(path string) bool {
	if !isRegular(path) {
		return false
	}
	cfg, ok := loadJSONObject(path)
	if !ok {
		return false
	}
	_, ok = cfg[mcpKey]
	return ok && isJSONObject(cfg[mcpKey])
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// isRegular reports a regular file that is not a symlink.
func isRegular(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}
