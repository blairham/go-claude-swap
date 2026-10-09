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
	"strings"
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

// SyncSharing mirrors ~/.claude into a profile, or undoes an earlier
// launch's mirroring. share governs SharedItems (customizations) and the
// user-scope mcpServers mirror; shareHistory governs the conversation
// history (projects/ and history.jsonl) — independent concerns, so
// --no-share --share-history gives a bare profile with unified history.
// Symlinks on macOS and Linux, so in-session /config changes land in
// ~/.claude; re-synced copies on Windows, where history is never shared (a
// copy would fork it). Only entries recorded in the manifest (or symlinks)
// are ever removed — user data the profile accumulated itself is never
// touched. Returns notes for the user (items not shared, and why).
func SyncSharing(dir string, share, shareHistory bool) []string {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	notes := syncMCPServers(dir, share)
	if runtime.GOOS == "windows" {
		shareHistory = false
	}
	// Always the default profile, even when CLAUDE_CONFIG_DIR is set here.
	sourceRoot := filepath.Join(home, ".claude")
	manifestPath := filepath.Join(dir, ShareManifest)
	managed := readManifest(manifestPath)

	var active []string
	if share {
		active = append(active, SharedItems...)
	}
	if shareHistory {
		active = append(active, historyItems...)
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
		return notes
	}

	useLinks := runtime.GOOS != "windows"
	var created []string
	for _, name := range active {
		src := filepath.Join(sourceRoot, name)
		dest := filepath.Join(dir, name)
		wasManaged := slices.Contains(managed, name)

		if slices.Contains(historyItems, name) {
			ok, n := prepareHistoryShare(src, dest, dir)
			notes = append(notes, n...)
			if !ok {
				continue
			}
		}

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

// prepareHistoryShare makes a history item linkable; false skips it this
// launch. The profile may already hold real history of its own: it is
// merged into ~/.claude first (never discarded — and even when the manifest
// claims the entry, since a stale manifest must not decide), and only while
// nothing runs in the profile, because the merge moves files out from under
// a running Claude Code. A missing source is created empty so there is
// something to link.
func prepareHistoryShare(src, dest, dir string) (bool, []string) {
	var notes []string
	name := filepath.Base(dest)
	if fi, err := os.Lstat(dest); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		if !Quiescent(dir) {
			return false, []string{fmt.Sprintf("Not sharing %s yet: another session is using this profile — retrying on the next launch.", name)}
		}
		if err := mergeHistoryIntoSource(src, dest); err != nil {
			return false, []string{fmt.Sprintf("Not sharing %s: merging the profile's existing history into %s failed: %v", name, src, err)}
		}
		notes = append(notes, fmt.Sprintf("Merged the profile's existing %s into %s — conversation history is now shared.", name, src))
	}
	if _, err := os.Stat(src); err != nil {
		// 0600/0700 to match Claude Code's own modes for history data.
		var cerr error
		if strings.HasSuffix(name, ".jsonl") {
			if cerr = os.MkdirAll(filepath.Dir(src), 0o700); cerr == nil {
				var f *os.File
				if f, cerr = os.OpenFile(src, os.O_CREATE|os.O_WRONLY, 0o600); cerr == nil {
					cerr = f.Close()
				}
			}
		} else {
			cerr = mkdirPrivate(src)
		}
		if cerr != nil {
			return false, append(notes, fmt.Sprintf("Not sharing %s: could not create %s: %v", name, src, cerr))
		}
	}
	return true, notes
}

// mergeHistoryIntoSource moves a profile's own history at dest into src.
// Directories merge file by file: transcript names are UUIDs, so a
// collision is the same session and the profile's duplicate is dropped.
// history.jsonl merges by appending the lines src does not already have.
// dest is removed once empty; a failure leaves what remains for next time.
func mergeHistoryIntoSource(src, dest string) error {
	fi, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		if err := mkdirPrivate(src); err != nil {
			return err
		}
		var all []string
		if err := filepath.WalkDir(dest, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p != dest {
				all = append(all, p)
			}
			return nil
		}); err != nil {
			return err
		}
		// Deepest first, so a directory is empty by the time it is reached.
		slices.Sort(all)
		slices.Reverse(all)
		for _, p := range all {
			rel, _ := filepath.Rel(dest, p)
			target := filepath.Join(src, rel)
			pfi, err := os.Lstat(p)
			if err != nil {
				return err
			}
			if pfi.IsDir() {
				if err := os.Remove(p); err != nil {
					return err
				}
				continue
			}
			if _, err := os.Lstat(target); err == nil {
				if err := os.Remove(p); err != nil {
					return err
				}
				continue
			}
			if err := mkdirPrivate(filepath.Dir(target)); err != nil {
				return err
			}
			if err := moveFile(p, target, pfi.Mode()); err != nil {
				return err
			}
		}
		return os.Remove(dest)
	}

	raw, err := os.ReadFile(dest)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	cur, err := os.ReadFile(src)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(cur), "\n") {
		have[l] = true
	}
	var add []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" && !have[l] {
			add = append(add, l)
		}
	}
	if len(add) > 0 {
		if err := os.MkdirAll(filepath.Dir(src), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(src, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		text := strings.Join(add, "\n") + "\n"
		// Never glue the first new line onto an unterminated last one.
		if len(cur) > 0 && cur[len(cur)-1] != '\n' {
			text = "\n" + text
		}
		_, werr := f.WriteString(text)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	return os.Remove(dest)
}

// moveFile renames, falling back to copy-and-remove across filesystems.
func moveFile(from, to string, mode os.FileMode) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	if mode&os.ModeSymlink != 0 {
		link, err := os.Readlink(from)
		if err != nil {
			return err
		}
		if err := os.Symlink(link, to); err != nil {
			return err
		}
		return os.Remove(from)
	}
	if err := copyFile(from, to, mode); err != nil {
		_ = os.Remove(to)
		return err
	}
	return os.Remove(from)
}

// mkdirPrivate is MkdirAll with 0700 on every level it creates (MkdirAll's
// mode is subject to umask, and history dirs should match Claude Code's).
func mkdirPrivate(p string) error {
	var missing []string
	for cur := p; ; cur = filepath.Dir(cur) {
		if _, err := os.Stat(cur); err == nil {
			break
		}
		missing = append(missing, cur)
		if filepath.Dir(cur) == cur {
			break
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if runtime.GOOS != "windows" {
			_ = os.Chmod(missing[i], 0o700)
		}
	}
	return nil
}
