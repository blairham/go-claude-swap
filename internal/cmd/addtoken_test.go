package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/credentials"
)

func cmdEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CSWAP_DISABLE_KEYCHAIN", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
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
