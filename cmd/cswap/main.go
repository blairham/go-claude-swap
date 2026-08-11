// Command cswap is a Go rewrite of claude-swap (realiti4/claude-swap): a
// multi-account manager for Claude Code. It captures the current Claude Code
// login into named accounts, switches between them safely (holding Claude
// Code's credential locks and refreshing tokens before activation), tracks
// usage across accounts, and can rotate automatically when the active
// account approaches its rate-limit windows.
package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/cmd"
)

func main() {
	c := cli.NewCLI("cswap", cmd.Version)
	c.Args = os.Args[1:]
	c.Commands = cmd.CommandFactory()

	exitStatus, err := c.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
	}

	os.Exit(exitStatus)
}
