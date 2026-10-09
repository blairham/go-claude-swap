package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/switcher"
)

// AddTokenCommand registers a setup-token or managed API key as an account.
type AddTokenCommand struct {
	UI cli.Ui
	// Stdin is where "-" reads the token from; nil means os.Stdin.
	Stdin io.Reader
}

// AddTokenFlags for cswap add-token.
type AddTokenFlags struct {
	Slot  int    `long:"slot" description:"Slot number to use (default: next free)"`
	Email string `long:"email" description:"Email to record for the account"`
	JSON  bool   `long:"json" description:"Emit JSON"`
}

// Help text.
func (c *AddTokenCommand) Help() string {
	return `Usage: cswap add-token [options] [<token>|-]

Register an account from a raw token instead of a Claude Code login — for
headless machines, or a token minted elsewhere. No network call is made.

  - A long-lived OAuth token from 'claude setup-token' (sk-ant-oat01-...)
    becomes an OAuth account. Setup-tokens have no refresh token and only
    the inference scope; when one expires, mint a new one and re-run this.
  - A managed API key (sk-ant-api...) becomes an API-key account, activated
    on Claude Code's API-key auth axis. Auto-switch skips API-key accounts
    unless autoswitch.includeApiKeyAccounts is set.

With no token argument you are prompted for it (input hidden); '-' reads one
line from stdin. Re-running with the same --email refreshes the account's
token in place.

Options:
      --slot NUM     Slot number to use (default: next free). Moves an
                     existing account with the same --email to that slot
      --email EMAIL  Email to record (default: setup-token-<slot>@token.local
                     or api-key-<slot>@token.local — tokens carry no identity)
      --json         Emit JSON
`
}

// Synopsis line.
func (c *AddTokenCommand) Synopsis() string {
	return "Register a setup-token or API key as an account"
}

// Run executes the command.
func (c *AddTokenCommand) Run(args []string) int {
	var opts AddTokenFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}
	fail := func(err error) int {
		if opts.JSON {
			return jsonError(c.UI, "AddTokenError", err)
		}
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	if len(remaining) > 1 {
		c.UI.Error(c.Help())
		return 1
	}

	token, err := c.readToken(remaining)
	if err != nil {
		return fail(err)
	}
	res, err := switcher.AddToken(token, opts.Email, opts.Slot)
	if err != nil {
		return fail(err)
	}

	kind := "setup-token"
	if res.APIKey {
		kind = "api_key"
	}
	if opts.JSON {
		doc := map[string]any{
			"account": accountRefJSON(res.Slot, res.Email),
			"kind":    kind,
			"updated": res.Updated,
		}
		if res.MovedFrom != 0 {
			doc["movedFrom"] = res.MovedFrom
		}
		return printJSON(c.UI, doc)
	}
	source := "token"
	if res.APIKey {
		source = "API key"
	}
	if res.Updated {
		c.UI.Output(fmt.Sprintf("Updated %s for Account-%d (%s [personal])", source, res.Slot, res.Email))
		return 0
	}
	if res.MovedFrom != 0 {
		c.UI.Output(fmt.Sprintf("Moved from slot %d → %d", res.MovedFrom, res.Slot))
	}
	c.UI.Output(fmt.Sprintf("Added Account-%d: %s [personal] (from %s)", res.Slot, res.Email, source))
	return 0
}

// readToken takes the token from the argument, stdin ("-"), or a hidden
// prompt — the last keeps it out of shell history.
func (c *AddTokenCommand) readToken(remaining []string) (string, error) {
	if len(remaining) == 0 {
		return c.UI.AskSecret("Token:")
	}
	if remaining[0] != "-" {
		return remaining[0], nil
	}
	in := c.Stdin
	if in == nil {
		in = os.Stdin
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading token from stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
