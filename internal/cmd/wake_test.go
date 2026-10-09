package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

// countWakes replaces the control-socket wake with a counter.
func countWakes(t *testing.T) *int {
	t.Helper()
	n := 0
	old := wakeEngine
	wakeEngine = func() { n++ }
	t.Cleanup(func() { wakeEngine = old })
	return &n
}

// fakeLogin stands in for Claude Code's /login on the file backend.
func fakeLogin(t *testing.T, email string) {
	t.Helper()
	home := os.Getenv("HOME")
	cfg := `{"oauthAccount":{"emailAddress":"` + email + `","accountUuid":"uuid-` + email + `","organizationUuid":""}}`
	cred := `{"claudeAiOauth":{"accessToken":"at","refreshToken":"rt","expiresAt":9999999999999}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(cred), 0o600); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, c cli.Command, ui *cli.MockUi, args ...string) {
	t.Helper()
	if code := c.Run(args); code != 0 {
		t.Fatalf("%T %v: exit %d: %s", c, args, code, ui.ErrorWriter.String())
	}
}

// #40: a roster change that can give the running auto loop a new candidate
// wakes it, so an account added during a long all-exhausted sleep is used
// now rather than up to an hour later.
func TestRosterChangesWakeTheLoop(t *testing.T) {
	cmdEnv(t)
	wakes := countWakes(t)
	ui := cli.NewMockUi()
	expect := func(what string, want int) {
		t.Helper()
		if *wakes != want {
			t.Fatalf("after %s: %d wakes, want %d", what, *wakes, want)
		}
	}

	fakeLogin(t, "a@example.com")
	run(t, &AddCommand{UI: ui}, ui)
	expect("add", 1)

	run(t, &AddTokenCommand{UI: ui}, ui, "--email", "t@example.com", "sk-ant-oat01-x")
	expect("add-token", 2)

	run(t, &DisableCommand{UI: ui, Disable: true}, ui, "1")
	expect("disable (never a reason to switch sooner)", 2)
	run(t, &DisableCommand{UI: ui, Disable: false}, ui, "1")
	expect("enable", 3)

	export := filepath.Join(t.TempDir(), "export.json")
	if _, err := switcher.Export(export, "test", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(paths.BackupRoot()); err != nil {
		t.Fatal(err)
	}
	run(t, &ImportCommand{UI: ui}, ui, export)
	expect("import", 4)
	run(t, &ImportCommand{UI: ui}, ui, export)
	expect("import that skipped every account", 4)
}

// A failed roster change leaves the loop alone.
func TestFailedRosterChangeDoesNotWake(t *testing.T) {
	cmdEnv(t)
	wakes := countWakes(t)
	ui := cli.NewMockUi()
	if code := (&AddCommand{UI: ui}).Run(nil); code == 0 {
		t.Fatal("add with no Claude Code login must fail")
	}
	if code := (&DisableCommand{UI: ui}).Run([]string{"7"}); code == 0 {
		t.Fatal("enable of an unknown account must fail")
	}
	if *wakes != 0 {
		t.Fatalf("%d wakes after failed commands, want 0", *wakes)
	}
}
