//go:build windows

package locks

import (
	"os"

	"golang.org/x/sys/windows"
)

func flockNB(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
}

func funlock(f *os.File) {
	ol := new(windows.Overlapped)
	windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
