// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

//go:build !windows && !linux

package logfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// RedirectStdio points the process's stdout and stderr at f, so output the
// program does not route through a File (a panic trace, a stray print)
// still lands in the current log.
func RedirectStdio(f *os.File) error {
	for _, fd := range []int{int(os.Stdout.Fd()), int(os.Stderr.Fd())} {
		if err := unix.Dup2(int(f.Fd()), fd); err != nil {
			return err
		}
	}
	return nil
}
