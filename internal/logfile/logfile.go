// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

// Package logfile is a size-bounded, self-rotating append-only log file for
// the long-running `cswap auto` service.
//
// The service managers that run the loop (Homebrew's launchd service and
// `cswap service install`) only know how to point stdout/stderr at a path;
// neither rotates it, and on macOS nothing else will either. So the process
// owns the file: it opens the path itself, rotates it by size (path → path.1
// → … → path.N), and — through the OnOpen hook — re-points its own
// stdout/stderr at each fresh file so stray output such as a panic trace
// lands in the current log rather than in a rotated one.
package logfile

import (
	"fmt"
	"os"
	"sync"
)

// Defaults: 10 MiB per file, three rotated generations — 40 MiB worst case.
const (
	DefaultMaxBytes = 10 << 20
	DefaultKeep     = 3
)

// File is an io.Writer that appends to Path and rotates when a write would
// take it past MaxBytes.
type File struct {
	Path     string
	MaxBytes int64
	Keep     int
	// OnOpen, if set, runs after every (re)open with the new file. It is how
	// the caller redirects the process's stdout/stderr.
	OnOpen func(*os.File) error

	mu   sync.Mutex
	f    *os.File
	size int64
}

// Open opens (creating if needed) path for appending with the default
// bounds; maxBytes or keep of zero or less select the defaults.
func Open(path string, maxBytes int64, keep int, onOpen func(*os.File) error) (*File, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if keep <= 0 {
		keep = DefaultKeep
	}
	lf := &File{Path: path, MaxBytes: maxBytes, Keep: keep, OnOpen: onOpen}
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if err := lf.open(); err != nil {
		return nil, err
	}
	// A file already over the cap (e.g. the unbounded log a previous
	// version left behind) is rotated straight away.
	if lf.size >= lf.MaxBytes {
		if err := lf.rotate(); err != nil {
			return nil, err
		}
	}
	return lf, nil
}

func (lf *File) open() error {
	f, err := os.OpenFile(lf.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if lf.OnOpen != nil {
		if err := lf.OnOpen(f); err != nil {
			f.Close()
			return err
		}
	}
	lf.f, lf.size = f, st.Size()
	return nil
}

// rotate shifts path.(N-1) → path.N … path → path.1 and opens a fresh path.
func (lf *File) rotate() error {
	if lf.f != nil {
		lf.f.Close()
		lf.f = nil
	}
	for i := lf.Keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", lf.Path, i), fmt.Sprintf("%s.%d", lf.Path, i+1))
	}
	if err := os.Rename(lf.Path, lf.Path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return lf.open()
}

// Write appends p, rotating first if p would push the file past MaxBytes.
// A single write is never split across files.
func (lf *File) Write(p []byte) (int, error) {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if lf.f == nil {
		if err := lf.open(); err != nil {
			return 0, err
		}
	}
	if lf.size > 0 && lf.size+int64(len(p)) > lf.MaxBytes {
		if err := lf.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := lf.f.Write(p)
	lf.size += int64(n)
	return n, err
}

// Close closes the current file.
func (lf *File) Close() error {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	if lf.f == nil {
		return nil
	}
	err := lf.f.Close()
	lf.f = nil
	return err
}
