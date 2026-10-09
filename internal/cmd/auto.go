// SPDX-FileCopyrightText: 2026 Blair Hamilton
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/autoswitch"
	"github.com/blairham/go-claude-swap/internal/logfile"
	"github.com/blairham/go-claude-swap/internal/notify"
	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/settings"
	"github.com/blairham/go-claude-swap/pkg/swapapi"
)

// AutoCommand runs the auto-switch loop (or a single tick with --once).
type AutoCommand struct {
	UI cli.Ui
}

// AutoFlags for cswap auto.
type AutoFlags struct {
	Once      bool    `long:"once"      description:"Run a single tick and exit (exit code = outcome)"`
	JSON      bool    `long:"json"      description:"Emit JSONL events"`
	DryRun    bool    `long:"dry-run"   description:"Decide but never switch"`
	Interval  float64 `long:"interval"  description:"Poll interval in seconds"`
	Threshold float64 `long:"threshold" description:"Switch threshold percent"`
	Cooldown  float64 `long:"cooldown"  description:"Minimum seconds between proactive switches"`
	Strategy  string  `long:"strategy"  description:"Target selection strategy"                                      choice:"best" choice:"consume-first" choice:"balance"`
	Model     string  `long:"model"     description:"Comma-separated model display names, 'auto', 'all', or 'none'"`
	LogFile   string  `long:"log-file"  description:"Append events to this file, rotated by size, instead of stdout"`
	Verbose   bool    `long:"verbose"   description:"Log every poll, not only changes and an hourly heartbeat"`
}

// Help text.
func (c *AutoCommand) Help() string {
	return `Usage: cswap auto [options]

Watch the active account's usage and switch automatically when its binding
window reaches the threshold (default 90%). The binding window is the worst
of 5h, 7d, and the watched models' weekly limits — by default the model
Claude Code is currently using (e.g. Fable), re-detected on every poll.
Runs in the foreground; use --once from cron or a systemd timer.

Exit codes with --once: 0 switched, 1 error, 2 no action, 3 blocked.

Options:
      --once             Run a single tick and exit
      --json             Emit JSONL events
      --dry-run          Decide but never switch
      --interval SECS    Poll interval (default from settings)
      --threshold PCT    Switch threshold (default 90)
      --cooldown SECS    Minimum seconds between proactive switches
      --strategy NAME    best | consume-first | balance
      --model NAMES      Model weekly limits to watch: names (Fable,Opus),
                         auto (Claude Code's model), all, or none
      --log-file PATH    Write events to PATH instead of stdout, rotating it
                         at 10 MiB and keeping 3 old files (PATH.1 .. PATH.3).
                         Stdout and stderr are redirected there too, so the
                         path a service manager logs to can be passed here
      --verbose          In loop mode, log every poll; by default an
                         unchanged poll is logged only once an hour
`
}

// Synopsis line.
func (c *AutoCommand) Synopsis() string {
	return "Automatically switch accounts near the rate limit"
}

// Run executes the command.
func (c *AutoCommand) Run(args []string) int {
	var opts AutoFlags
	_, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}

	cfg := autoswitch.ConfigFromSettings()
	if opts.Interval > 0 {
		cfg.Interval = opts.Interval
	}
	if opts.Threshold > 0 {
		cfg.Threshold = opts.Threshold
	}
	if opts.Cooldown > 0 {
		cfg.Cooldown = opts.Cooldown
	}
	if opts.Strategy != "" {
		cfg.Strategy = opts.Strategy
	}
	if opts.Model != "" {
		cfg.SetModels(opts.Model)
	}
	cfg.DryRun = opts.DryRun

	out := io.Writer(os.Stdout)
	if opts.LogFile != "" {
		lf, err := logfile.Open(opts.LogFile, 0, 0, logfile.RedirectStdio)
		if err != nil {
			c.UI.Error("Error: opening log file: " + err.Error())
			return 1
		}
		defer lf.Close()
		out = lf
	}

	var sink autoswitch.EventSink
	if opts.JSON {
		sink = autoswitch.NewJSONSink(out)
	} else {
		sink = autoswitch.NewHumanSink(out)
		// A long-running loop's log is mostly identical below-threshold
		// ticks; keep only the ones that say something new. JSONL stays
		// complete: its consumers may treat every poll as a liveness tick.
		if !opts.Once && !opts.Verbose {
			sink = autoswitch.NewQuietSink(sink, autoswitch.DefaultHeartbeat)
		}
	}

	// Outermost, so it sees the polls QuietSink drops: recovery is read
	// from them.
	sink = autoswitch.NewNotifySink(sink, settings.Load().String("autoswitch.notify"), notify.Desktop())

	if opts.Once {
		return int(autoswitch.NewEngine(cfg, sink).RunOnce())
	}

	// Loop mode serves the gRPC control API on a unix socket so the TUI can
	// see status, follow events, and stay store-only while we fetch.
	bcast := swapapi.NewBroadcast(sink)
	engine := autoswitch.NewEngine(cfg, bcast)
	if srv, serr := swapapi.Serve(paths.SocketPath(), engine, bcast, Version); serr != nil {
		c.UI.Warn("control API unavailable (" + serr.Error() + "); continuing without it")
	} else {
		defer srv.Stop()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := engine.Run(ctx); err != nil && ctx.Err() == nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	return 0
}
