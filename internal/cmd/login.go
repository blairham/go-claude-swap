package cmd

import (
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/oauth"
	"github.com/blairham/go-claude-swap/internal/switcher"
)

// LoginCommand re-authenticates an account (or adds a new one) via the
// OAuth flow, without touching the live Claude Code session unless the
// account being re-logged-in is the active one.
type LoginCommand struct {
	UI cli.Ui
}

// LoginFlags for cswap login.
type LoginFlags struct {
	NoBrowser bool `long:"no-browser" description:"Print the login URL instead of opening a browser"`
}

// Help text.
func (c *LoginCommand) Help() string {
	return `Usage: cswap login [options] [<number|alias|email>]

Log an account in through Claude's OAuth flow and store the credentials in
its slot. Use it to repair an account whose stored token has died, or to add
a new account without logging the current one out.

A browser opens on Claude's login page; after approving, paste the code it
shows back into the terminal. With an argument, the login must match that
account. If the account is the currently active one, the live credential is
replaced too; otherwise the live Claude Code session is left untouched.

Options:
      --no-browser  Print the login URL instead of opening a browser
`
}

// Synopsis line.
func (c *LoginCommand) Synopsis() string {
	return "Re-authenticate or add an account via the OAuth flow"
}

// Run executes the command.
func (c *LoginCommand) Run(args []string) int {
	var opts LoginFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	selector := ""
	if len(remaining) > 1 {
		c.UI.Error("Usage: cswap login [<number|alias|email>]")
		return 1
	}
	if len(remaining) == 1 {
		selector = remaining[0]
	}

	pkce, err := oauth.NewPKCE()
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	loginURL := pkce.AuthorizeRequestURL()

	c.UI.Output("Log in on Claude's login page, then paste the code it shows you here.")
	if selector != "" {
		c.UI.Output(fmt.Sprintf("Make sure to log in as the account for %q.", selector))
	}
	c.UI.Output("")
	c.UI.Output("  " + loginURL)
	c.UI.Output("")
	if !opts.NoBrowser {
		openBrowser(loginURL)
	}

	pasted, err := c.UI.Ask("Authorization code:")
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}

	outcome := oauth.ExchangeCode(http.DefaultClient, pasted, pkce, time.Now)
	switch outcome.Err {
	case oauth.ErrNone:
	case oauth.ErrStateMismatch:
		c.UI.Error("Error: the pasted code does not belong to this login attempt — run 'cswap login' again and paste the fresh code")
		return 1
	case oauth.ErrTransient:
		c.UI.Error("Error: the token exchange failed (network problem or expired code) — try again")
		return 1
	default:
		c.UI.Error(fmt.Sprintf("Error: the token endpoint rejected the code (%s)", outcome.Err))
		return 1
	}

	raw, err := oauth.MarshalBlob(outcome.Blob)
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	res, err := switcher.StoreLogin(selector, string(raw), outcome.Identity)
	if err != nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}

	if res.Created {
		c.UI.Output(fmt.Sprintf("Added Account-%d (%s)", res.Slot, res.Email))
	} else {
		c.UI.Output(fmt.Sprintf("Re-authenticated Account-%d (%s)", res.Slot, res.Email))
	}
	if res.Activated {
		c.UI.Output("It is the active account, so the live credential was updated too.")
		c.UI.Output("Restart Claude Code to apply immediately — a running instance picks it up within ~30s.")
	} else {
		c.UI.Output(fmt.Sprintf("Run 'cswap switch %d' to make it the active account.", res.Slot))
	}
	return 0
}

// openBrowser is best-effort: the URL is already printed for manual use.
func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", url).Start()
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
}
