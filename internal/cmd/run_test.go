package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/mappings"
	"github.com/blairham/go-claude-swap/internal/session"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

type launched struct {
	bin  string
	args []string
	env  []string
}

// stubLaunch replaces exec and PATH lookup so no real claude ever runs.
func stubLaunch(t *testing.T) *launched {
	t.Helper()
	got := &launched{}
	prevL, prevP, prevProbe := launchClaude, lookPath, session.Probe
	launchClaude = func(bin string, args, env []string) (int, error) {
		got.bin, got.args, got.env = bin, args, env
		return 0, nil
	}
	lookPath = func(string) (string, error) { return "/fake/claude", nil }
	session.Probe = func(dir string) (session.Verdict, session.Status) {
		in := session.ReadCredentials(dir) != ""
		email, _, _ := session.Identity(dir)
		return session.Valid, session.Status{LoggedIn: &in, AuthMethod: "claude.ai", Email: email}
	}
	t.Cleanup(func() { launchClaude, lookPath, session.Probe = prevL, prevP, prevProbe })
	return got
}

func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, _ := strings.Cut(kv, "="); k == name {
			return v, true
		}
	}
	return "", false
}

// addAccount captures email as a managed account the way 'cswap add' does.
func addAccount(t *testing.T, email string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	cfg, _ := json.Marshal(map[string]any{"oauthAccount": map[string]any{"emailAddress": email, "organizationUuid": nil}})
	os.WriteFile(filepath.Join(home, ".claude.json"), cfg, 0o600)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"at-`+email+`","refreshToken":"rt-`+email+`","expiresAt":9999999999000}}`), 0o600)
	if _, _, err := switcher.Add(0, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRunLaunchesSessionProfile(t *testing.T) {
	cmdEnv(t)
	got := stubLaunch(t)
	addAccount(t, "a@b.co")
	addAccount(t, "c@d.co") // now the default login
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-api03-hijack")

	ui := cli.NewMockUi()
	if code := (&RunCommand{UI: ui}).Run([]string{"a@b.co", "--", "--resume", "x"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ui.ErrorWriter.String())
	}
	if dir, ok := envValue(got.env, "CLAUDE_CONFIG_DIR"); !ok || dir != session.Dir(1, "a@b.co") {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", dir)
	}
	if _, ok := envValue(got.env, "ANTHROPIC_API_KEY"); ok {
		t.Fatal("auth override leaked into the session")
	}
	if !slices.Equal(got.args, []string{"--resume", "x"}) || got.bin != "/fake/claude" {
		t.Fatalf("launched %s %v", got.bin, got.args)
	}
	if !strings.Contains(ui.ErrorWriter.String(), "Ignoring ANTHROPIC_API_KEY") {
		t.Fatalf("no scrub warning: %s", ui.ErrorWriter.String())
	}
}

func TestRunUsesDirectoryMapping(t *testing.T) {
	cmdEnv(t)
	got := stubLaunch(t)
	addAccount(t, "a@b.co")
	addAccount(t, "c@d.co")
	home, _ := os.UserHomeDir()
	repo := filepath.Join(home, "repo", "src")
	os.MkdirAll(repo, 0o700)
	t.Chdir(repo)

	// Unmapped: plain claude, environment untouched.
	ui := cli.NewMockUi()
	if code := (&RunCommand{UI: ui}).Run(nil); code != 0 {
		t.Fatalf("exit %d: %s", code, ui.ErrorWriter.String())
	}
	if dir, _ := envValue(got.env, "CLAUDE_CONFIG_DIR"); dir != "" {
		t.Fatal("unmapped directory launched a session profile")
	}

	mappings.Open().Set(filepath.Dir(repo), "a@b.co", "")
	ui = cli.NewMockUi()
	if code := (&RunCommand{UI: ui}).Run(nil); code != 0 {
		t.Fatalf("exit %d: %s", code, ui.ErrorWriter.String())
	}
	if dir, _ := envValue(got.env, "CLAUDE_CONFIG_DIR"); dir != session.Dir(1, "a@b.co") {
		t.Fatalf("mapped launch used %q", dir)
	}
}

func TestRunRejectsStrayArguments(t *testing.T) {
	cmdEnv(t)
	stubLaunch(t)
	ui := cli.NewMockUi()
	if code := (&RunCommand{UI: ui}).Run([]string{"1", "--resume"}); code == 0 {
		t.Fatal("claude flags before -- were accepted")
	}
}
