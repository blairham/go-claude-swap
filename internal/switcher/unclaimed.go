package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// UnclaimedEntry is one stashed credential.
type UnclaimedEntry struct {
	ID        string
	CreatedAt string
	Reason    string
	Slot      string
}

// ListUnclaimed returns stash rows, manifest-backed first; orphan entry
// files (no manifest row) are listed with empty metadata.
func ListUnclaimed() ([]UnclaimedEntry, error) {
	manifestPath := filepath.Join(paths.CredentialsDir(), ".unclaimed-manifest.json")
	entries := map[string]UnclaimedEntry{}
	if raw, err := os.ReadFile(manifestPath); err == nil {
		var m struct {
			Entries map[string]struct {
				CreatedAt  string `json:"createdAt"`
				Reason     string `json:"reason"`
				ConfigSlot string `json:"configSlot"`
			} `json:"entries"`
		}
		if json.Unmarshal(raw, &m) == nil {
			for id, e := range m.Entries {
				entries[id] = UnclaimedEntry{ID: id, CreatedAt: e.CreatedAt, Reason: e.Reason, Slot: e.ConfigSlot}
			}
		}
	}
	files, err := filepath.Glob(filepath.Join(paths.CredentialsDir(), ".unclaimed-*.enc"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), ".unclaimed-"), ".enc")
		if id == "manifest" {
			continue
		}
		if _, ok := entries[id]; !ok {
			entries[id] = UnclaimedEntry{ID: id, Reason: "orphaned (no manifest row)"}
		}
	}
	out := make([]UnclaimedEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// PurgeUnclaimed deletes one stash entry (file + manifest row).
func PurgeUnclaimed(id string) error {
	entryPath := filepath.Join(paths.CredentialsDir(), ".unclaimed-"+id+".enc")
	fileErr := os.Remove(entryPath)
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		return fileErr
	}

	manifestPath := filepath.Join(paths.CredentialsDir(), ".unclaimed-manifest.json")
	removedRow := false
	if raw, err := os.ReadFile(manifestPath); err == nil {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			if entries, ok := m["entries"].(map[string]any); ok {
				if _, present := entries[id]; present {
					delete(entries, id)
					removedRow = true
					if data, merr := json.MarshalIndent(m, "", "  "); merr == nil {
						os.WriteFile(manifestPath, data, 0o600)
					}
				}
			}
		}
	}
	if errors.Is(fileErr, os.ErrNotExist) && !removedRow {
		return fmt.Errorf("no unclaimed entry %q", id)
	}
	return nil
}
