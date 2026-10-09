// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"strings"
	"testing"

	"github.com/blairham/go-claude-swap/pkg/swapapi"
)

func TestControlSocketLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := probeControl
	t.Cleanup(func() { probeControl = old })

	probeControl = func() (*swapapi.GetStatusResponse, bool) { return nil, false }
	if got := controlSocketLine(); !strings.Contains(got, "cswap.sock: no answer") {
		t.Errorf("down socket: %q", got)
	}

	probeControl = func() (*swapapi.GetStatusResponse, bool) {
		return &swapapi.GetStatusResponse{Version: "1.2.3", Strategy: "best", DryRun: true}, true
	}
	got := controlSocketLine()
	for _, want := range []string{"cswap.sock: answering", "cswap 1.2.3", "strategy best", "dry run"} {
		if !strings.Contains(got, want) {
			t.Errorf("live socket line missing %q: %q", want, got)
		}
	}
}
