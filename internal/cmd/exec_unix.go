// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package cmd

import "syscall"

// execClaude replaces cswap with claude; it returns only when exec fails.
// No cswap lock is held here — an exec'd claude must never inherit one.
func execClaude(bin string, args, env []string) (int, error) {
	return 1, syscall.Exec(bin, append([]string{bin}, args...), env)
}
