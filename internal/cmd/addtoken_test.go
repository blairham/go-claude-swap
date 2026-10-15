// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/credentials"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

func cmdEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CSWAP_DISABLE_KEYCHAIN", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	// Never reach for a control socket, even the temp HOME's.
	old := wakeEngine
	wakeEngine = func() {}
	t.Cleanup(func() { wakeEngine = old })
	// Never reach the real token endpoint: `cswap run` spends a refresh
	// before seeding a profile, and these fixtures' tokens are fake.
	prevClient := switcher.RefreshClient
	switcher.RefreshClient = &http.Client{Transport: unreachableEndpoint{}}
	t.Cleanup(func() { switcher.RefreshClient = prevClient })
}

// unreachableEndpoint answers every token request 503 (transient).
type unreachableEndpoint struct{}

func (unreachableEndpoint) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
}

func TestAddTokenReadsStdinDash(t *testing.T) {
	cmdEnv(t)
	ui := cli.NewMockUi()
	c := &AddTokenCommand{UI: ui, Stdin: strings.NewReader("sk-ant-oat01-piped\r\nignored\n")}
	if code := c.Run([]string{"--json", "--email", "ci@example.com", "-"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ui.ErrorWriter.String())
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(ui.OutputWriter.String()), &doc); err != nil {
		t.Fatalf("output %q: %v", ui.OutputWriter.String(), err)
	}
	if doc["kind"] != "setup-token" || doc["schemaVersion"] != float64(1) {
		t.Fatalf("envelope = %v", doc)
	}
	cred, _ := credentials.ReadBackup(1, "ci@example.com")
	if !strings.Contains(cred, `"accessToken": "sk-ant-oat01-piped"`) {
		t.Fatalf("stored = %q", cred)
	}
}

func TestAddTokenPromptsWhenNoArgument(t *testing.T) {
	cmdEnv(t)
	ui := cli.NewMockUi()
	ui.InputReader = strings.NewReader("sk-ant-api03-prompted\n")
	c := &AddTokenCommand{UI: ui}
	if code := c.Run(nil); code != 0 {
		t.Fatalf("exit %d: %s", code, ui.ErrorWriter.String())
	}
	if cred, _ := credentials.ReadBackup(1, "api-key-1@token.local"); cred != "sk-ant-api03-prompted" {
		t.Fatalf("stored = %q", cred)
	}
}
