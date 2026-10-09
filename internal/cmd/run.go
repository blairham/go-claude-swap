package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/session"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

// launchClaude hands the terminal to claude and returns only on failure
// (or, on Windows, with claude's exit code). A variable so tests never
// launch the real claude.
var launchClaude = execClaude

// lookPath finds claude; a variable for tests.
var lookPath = exec.LookPath

// RunCommand launches Claude Code as a stored account in this terminal.
type RunCommand struct {
	UI cli.Ui
}

// RunFlags for cswap run.
type RunFlags struct {
	NoShare        bool `long:"no-share" description:"Don't share ~/.claude customizations into the session profile"`
	ShareHistory   bool `long:"share-history" description:"Share ~/.claude conversation history (projects/, history.jsonl) with the session"`
	RequireSession bool `long:"require-session" description:"Refuse to launch on the default login"`
}

// Help text.
func (c *RunCommand) Help() string {
	return `Usage: cswap run [options] [<number|alias|email>] [-- <claude args>...]

Launch Claude Code as a stored account in this terminal only. The account
gets its own persistent profile under <backup dir>/sessions/, used as
CLAUDE_CONFIG_DIR, so the default login — and every other terminal and the
IDE extension — is left untouched. Profiles are shared with the Python
claude-swap's 'cswap run'.

With no account, the current directory's mapping decides (see 'cswap map');
an unmapped directory launches plain claude on the default login.

By default settings.json, keybindings.json, CLAUDE.md, skills/, commands/
and agents/ from ~/.claude are linked into the profile. Variables that
would override the account (ANTHROPIC_API_KEY and friends) are dropped for
the session. User-scope MCP servers (mcpServers in ~/.claude.json) are
mirrored into the profile; definitions the profile had of its own are saved
once to .cswap-mcp-displaced.json in the profile.

--share-history additionally links ~/.claude/projects (what 'claude
--resume' lists) and ~/.claude/history.jsonl, so every account sees one
conversation history. History the profile already accumulated is merged
into ~/.claude first (only while no Claude Code runs in the profile).
Not supported on Windows.

Examples:
  cswap run 2
  cswap run work -- --resume
  cswap run 2 --share-history
  cswap run                           # use this directory's mapping

Options:
      --no-share         Don't share ~/.claude customizations or MCP
                         servers into the profile (and remove previously
                         shared ones)
      --share-history    Share ~/.claude conversation history with the
                         profile
      --require-session  Refuse to launch when the account is already the
                         default login, instead of running plain claude on it
`
}

// Synopsis line.
func (c *RunCommand) Synopsis() string {
	return "Run Claude Code as an account in this terminal only"
}

// Run executes the command.
func (c *RunCommand) Run(args []string) int {
	head, tail := args, []string(nil)
	if i := slices.Index(args, "--"); i >= 0 {
		head, tail = args[:i], args[i+1:]
	}
	var opts RunFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, head)
	if stop {
		return code
	}
	if len(remaining) > 1 {
		c.UI.Error("Error: unexpected arguments " + strings.Join(remaining[1:], " ") + " (pass claude's own arguments after --)")
		return 1
	}

	if opts.ShareHistory && runtime.GOOS == "windows" {
		c.UI.Error("Error: --share-history is not supported on Windows yet: sharing uses re-synced copies there, which would fork the history instead of sharing it.")
		return 1
	}

	claude, err := lookPath("claude")
	if err != nil {
		c.UI.Error("Error: 'claude' was not found on PATH. Install Claude Code first.")
		return 1
	}

	selector := ""
	if len(remaining) == 1 {
		selector = remaining[0]
	} else {
		cwd, _ := os.Getwd()
		slot, email, merr := switcher.SlotForDirectory(cwd)
		switch {
		case merr != nil:
			c.UI.Error("Error: " + merr.Error())
			return 1
		case slot != 0:
			selector = fmt.Sprint(slot)
		case email != "":
			c.UI.Warn(fmt.Sprintf("Mapped account %s no longer exists — launching the default account.", email))
			return c.launch(claude, tail, os.Environ())
		default:
			c.UI.Output(fmt.Sprintf("No account mapped for %s — launching the default account.", cwd))
			return c.launch(claude, tail, os.Environ())
		}
	}

	plan, err := switcher.PrepareSession(selector, switcher.SessionOptions{
		Share:          !opts.NoShare,
		ShareHistory:   opts.ShareHistory,
		RequireSession: opts.RequireSession,
	})
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	for _, n := range plan.Notes {
		c.UI.Warn(n)
	}
	if plan.Direct {
		return c.launch(claude, tail, os.Environ())
	}

	var scrubbed []string
	for _, v := range session.AuthOverrideEnv {
		if os.Getenv(v) != "" {
			scrubbed = append(scrubbed, v)
		}
	}
	if len(scrubbed) > 0 {
		c.UI.Warn(fmt.Sprintf("Ignoring %s for this session — it would override the selected account inside Claude Code.", strings.Join(scrubbed, ", ")))
	}
	c.UI.Output(fmt.Sprintf("Launching Account-%d (%s) [session mode]", plan.Slot, plan.Email))
	env := append(session.ScrubbedEnv(os.Environ()), "CLAUDE_CONFIG_DIR="+plan.Dir)
	return c.launch(claude, tail, env)
}

func (c *RunCommand) launch(claude string, args, env []string) int {
	code, err := launchClaude(claude, args, env)
	if err != nil {
		c.UI.Error("Error: launching claude: " + err.Error())
		return 1
	}
	return code
}
