// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package session

import (
	"errors"
	"syscall"
)

// processAlive reports whether pid names a running process. EPERM means it
// exists but belongs to someone else.
func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
