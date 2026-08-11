// Package locks provides the two lock protocols a switch must hold: cswap's
// own flock-based file lock, and Claude Code's proper-lockfile-compatible
// directory locks (mkdir is the mutex, mtime is the heartbeat).
package locks

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

// ErrTimeout is returned when a lock cannot be acquired in time.
var ErrTimeout = errors.New("lock acquisition timed out")

// FileLock is an OS-level exclusive lock on a file, polled non-blocking.
// It is not re-entrant.
type FileLock struct {
	Path    string
	Timeout time.Duration

	f *os.File
}

// NewFileLock creates a lock on path with the default 10s timeout.
func NewFileLock(path string) *FileLock {
	return &FileLock{Path: path, Timeout: 10 * time.Second}
}

// Acquire takes the lock, polling every 100ms until Timeout.
func (l *FileLock) Acquire() error {
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(l.Timeout)
	for {
		err := flockNB(f)
		if err == nil {
			l.f = f
			return nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return fmt.Errorf("%w: %s", ErrTimeout, l.Path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Release drops the lock. The lock file is left on disk.
func (l *FileLock) Release() {
	if l.f != nil {
		funlock(l.f)
		l.f.Close()
		l.f = nil
	}
}

// DirLock implements npm proper-lockfile's protocol: the lock artifact is a
// directory; mkdir atomicity is the mutex. While held, a goroutine touches
// the dir mtime every 3s; a dir whose mtime is older than Staleness may be
// stolen (rmdir + retry).
type DirLock struct {
	Path      string
	Staleness time.Duration
	Timeout   time.Duration

	stopTouch chan struct{}
}

// Acquire takes the directory lock.
func (l *DirLock) Acquire() error {
	deadline := time.Now().Add(l.Timeout)
	for {
		err := os.Mkdir(l.Path, 0o755)
		if err == nil {
			l.stopTouch = make(chan struct{})
			go l.touchLoop(l.stopTouch)
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if st, serr := os.Stat(l.Path); serr == nil && time.Since(st.ModTime()) > l.Staleness {
			// Stale holder: steal and retry immediately.
			os.Remove(l.Path)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s (is Claude Code busy?)", ErrTimeout, l.Path)
		}
		time.Sleep(time.Duration(250+rand.Intn(250)) * time.Millisecond)
	}
}

func (l *DirLock) touchLoop(stop <-chan struct{}) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			now := time.Now()
			_ = os.Chtimes(l.Path, now, now)
		}
	}
}

// Release stops the heartbeat and removes the lock directory.
func (l *DirLock) Release() {
	if l.stopTouch != nil {
		close(l.stopTouch)
		l.stopTouch = nil
	}
	os.Remove(l.Path)
}

// ClaudeCredentialLocks returns Claude Code's credential locks in CC's own
// acquisition order: <config_home>/.oauth_refresh.lock, then the legacy
// sibling <config_home>.lock. Matching the pair and order prevents deadlock
// with a concurrently-refreshing Claude Code.
func ClaudeCredentialLocks(configHome string) []*DirLock {
	return []*DirLock{
		{Path: filepath.Join(configHome, ".oauth_refresh.lock"), Staleness: 60 * time.Second, Timeout: 9 * time.Second},
		{Path: configHome + ".lock", Staleness: 60 * time.Second, Timeout: 9 * time.Second},
	}
}

// ClaudeConfigLock returns the lock guarding ~/.claude.json writes.
func ClaudeConfigLock(globalConfigPath string) *DirLock {
	return &DirLock{Path: globalConfigPath + ".lock", Staleness: 10 * time.Second, Timeout: 9 * time.Second}
}

// AcquireAll takes a set of DirLocks in order, releasing any already held
// on failure.
func AcquireAll(ls []*DirLock) error {
	for i, l := range ls {
		if err := l.Acquire(); err != nil {
			for j := i - 1; j >= 0; j-- {
				ls[j].Release()
			}
			return err
		}
	}
	return nil
}

// ReleaseAll releases DirLocks in reverse order.
func ReleaseAll(ls []*DirLock) {
	for i := len(ls) - 1; i >= 0; i-- {
		ls[i].Release()
	}
}
