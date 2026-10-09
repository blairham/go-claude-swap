package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
)

type manifest struct {
	Items []string `json:"items"`
	Mode  string   `json:"mode"`
}

// readManifest returns the items cswap created in a profile — only names it
// could have created, whatever the file says.
func readManifest(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m manifest
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var out []string
	for _, it := range m.Items {
		if slices.Contains(SharedItems, it) || slices.Contains(historyItems, it) {
			out = append(out, it)
		}
	}
	return out
}

// SyncSharing mirrors SharedItems from ~/.claude into a profile (share) or
// removes what an earlier launch mirrored (!share). Symlinks on macOS and
// Linux, so in-session /config changes land in ~/.claude; re-synced copies
// on Windows. Only entries recorded in the manifest (or symlinks) are ever
// removed — user data the profile accumulated itself is never touched.
// Returns notes for the user (items not shared, and why).
func SyncSharing(dir string, share bool) []string {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	// Always the default profile, even when CLAUDE_CONFIG_DIR is set here.
	sourceRoot := filepath.Join(home, ".claude")
	manifestPath := filepath.Join(dir, ShareManifest)
	managed := readManifest(manifestPath)

	var active []string
	if share {
		active = SharedItems
	}
	for _, name := range managed {
		if slices.Contains(active, name) {
			continue
		}
		dest := filepath.Join(dir, name)
		// History items are only ever unlinked, never deleted: a stale
		// manifest must not be able to take real conversation history.
		if slices.Contains(historyItems, name) && !isSymlink(dest) {
			continue
		}
		removeManaged(dest)
	}
	if len(active) == 0 {
		_ = os.Remove(manifestPath)
		return nil
	}

	useLinks := runtime.GOOS != "windows"
	var notes, created []string
	for _, name := range active {
		src := filepath.Join(sourceRoot, name)
		dest := filepath.Join(dir, name)
		wasManaged := slices.Contains(managed, name)

		if _, err := os.Stat(src); err != nil {
			if wasManaged {
				removeManaged(dest)
			}
			continue
		}
		// Link to the fully resolved target: a link to a link lets Claude
		// Code's atomic settings write replace the intermediate link with a
		// regular file.
		target := src
		if r, err := filepath.EvalSymlinks(src); err == nil {
			target = r
		}

		if isSymlink(dest) {
			if useLinks {
				if cur, err := os.Readlink(dest); err == nil && cur == target {
					created = append(created, name)
					continue
				}
				_ = os.Remove(dest)
			} else {
				_ = os.Remove(dest) // moved POSIX → Windows: replace with a copy
			}
		} else if _, err := os.Lstat(dest); err == nil && !wasManaged {
			notes = append(notes, fmt.Sprintf("Not sharing %s: the session profile already has its own copy.", name))
			continue
		} else if err == nil {
			removeManaged(dest)
		}

		var lerr error
		if useLinks {
			lerr = os.Symlink(target, dest)
		} else {
			lerr = copyTree(src, dest)
		}
		if lerr != nil {
			notes = append(notes, fmt.Sprintf("Could not share %s: %v", name, lerr))
			continue
		}
		created = append(created, name)
	}

	mode := "symlink"
	if !useLinks {
		mode = "copy"
	}
	if data, err := json.MarshalIndent(manifest{Items: nonNil(created), Mode: mode}, "", "  "); err == nil {
		_ = writePrivate(manifestPath, data)
	}
	return notes
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// removeManaged removes a cswap-created share entry: a link, a file, or a
// copied directory.
func removeManaged(dest string) {
	fi, err := os.Lstat(dest)
	if err != nil {
		return
	}
	if fi.IsDir() {
		_ = os.RemoveAll(dest)
		return
	}
	_ = os.Remove(dest)
}

// copyTree copies a file or directory (Windows share mode).
func copyTree(src, dest string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return copyFile(src, dest, fi.Mode())
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o700)
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		return copyFile(p, out, info.Mode())
	})
}

func copyFile(src, dest string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	if err := out.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil && !errors.Is(cerr, io.EOF) {
		return cerr
	}
	return nil
}
