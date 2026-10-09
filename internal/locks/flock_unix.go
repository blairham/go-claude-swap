// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package locks

import (
	"os"
	"syscall"
)

func flockNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func funlock(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
