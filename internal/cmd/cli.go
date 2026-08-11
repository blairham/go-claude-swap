// Package cmd wires the cswap CLI: hashicorp/cli command dispatch with
// go-flags option parsing per command.
package cmd

import (
	"os"

	"github.com/hashicorp/cli"
)

// Version is stamped via -ldflags at build time.
var Version = "dev"

// CommandFactory creates CLI commands.
func CommandFactory() map[string]cli.CommandFactory {
	ui := &cli.BasicUi{
		Reader:      os.Stdin,
		Writer:      os.Stdout,
		ErrorWriter: os.Stderr,
	}

	return map[string]cli.CommandFactory{
		"list": func() (cli.Command, error) {
			return &ListCommand{UI: ui}, nil
		},
		"ls": func() (cli.Command, error) {
			return &ListCommand{UI: ui}, nil
		},
		"status": func() (cli.Command, error) {
			return &StatusCommand{UI: ui}, nil
		},
		"switch": func() (cli.Command, error) {
			return &SwitchCommand{UI: ui}, nil
		},
		"add": func() (cli.Command, error) {
			return &AddCommand{UI: ui}, nil
		},
		"remove": func() (cli.Command, error) {
			return &RemoveCommand{UI: ui}, nil
		},
		"rm": func() (cli.Command, error) {
			return &RemoveCommand{UI: ui}, nil
		},
		"disable": func() (cli.Command, error) {
			return &DisableCommand{UI: ui, Disable: true}, nil
		},
		"enable": func() (cli.Command, error) {
			return &DisableCommand{UI: ui, Disable: false}, nil
		},
		"alias": func() (cli.Command, error) {
			return &AliasCommand{UI: ui}, nil
		},
		"move": func() (cli.Command, error) {
			return &MoveCommand{UI: ui}, nil
		},
		"config": func() (cli.Command, error) {
			return &ConfigCommand{UI: ui}, nil
		},
		"export": func() (cli.Command, error) {
			return &ExportCommand{UI: ui}, nil
		},
		"import": func() (cli.Command, error) {
			return &ImportCommand{UI: ui}, nil
		},
		"unclaimed": func() (cli.Command, error) {
			return &UnclaimedCommand{UI: ui}, nil
		},
		"auto": func() (cli.Command, error) {
			return &AutoCommand{UI: ui}, nil
		},
		"service": func() (cli.Command, error) {
			return &ServiceCommand{UI: ui}, nil
		},
		"tui": func() (cli.Command, error) {
			return &TUICommand{UI: ui, Page: "dashboard"}, nil
		},
		"watch": func() (cli.Command, error) {
			return &TUICommand{UI: ui, Page: "watch"}, nil
		},
	}
}
