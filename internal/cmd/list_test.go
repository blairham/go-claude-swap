package cmd

import (
	"strings"
	"testing"

	"github.com/hashicorp/cli"
)

func TestListTokenStatusRejectsJSON(t *testing.T) {
	cmdEnv(t)
	ui := cli.NewMockUi()
	if code := (&ListCommand{UI: ui}).Run([]string{"--token-status", "--json"}); code == 0 {
		t.Fatal("--token-status --json accepted")
	}
	if !strings.Contains(ui.ErrorWriter.String(), "cannot be combined") {
		t.Fatalf("stderr = %q", ui.ErrorWriter.String())
	}
}
