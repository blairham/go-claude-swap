package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/autoswitch"
)

// AutoCommand runs the auto-switch loop (or a single tick with --once).
type AutoCommand struct {
	UI cli.Ui
}

// AutoFlags for cswap auto.
type AutoFlags struct {
	Once      bool    `long:"once" description:"Run a single tick and exit (exit code = outcome)"`
	JSON      bool    `long:"json" description:"Emit JSONL events"`
	DryRun    bool    `long:"dry-run" description:"Decide but never switch"`
	Interval  float64 `long:"interval" description:"Poll interval in seconds"`
	Threshold float64 `long:"threshold" description:"Switch threshold percent"`
	Cooldown  float64 `long:"cooldown" description:"Minimum seconds between proactive switches"`
	Strategy  string  `long:"strategy" choice:"best" choice:"consume-first" description:"Target selection strategy"`
	Model     string  `long:"model" description:"Comma-separated model display names (or 'all')"`
}

// Help text.
func (c *AutoCommand) Help() string {
	return `Usage: cswap auto [options]

Watch the active account's usage and switch automatically when its binding
5h/7d window reaches the threshold (default 90%). Runs in the foreground;
use --once from cron or a systemd timer.

Exit codes with --once: 0 switched, 1 error, 2 no action, 3 blocked.

Options:
      --once             Run a single tick and exit
      --json             Emit JSONL events
      --dry-run          Decide but never switch
      --interval SECS    Poll interval (default from settings)
      --threshold PCT    Switch threshold (default 90)
      --cooldown SECS    Minimum seconds between proactive switches
      --strategy NAME    best | consume-first
      --model NAMES      Also watch these models' weekly limits
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

	var sink autoswitch.EventSink
	if opts.JSON {
		sink = autoswitch.NewJSONSink(os.Stdout)
	} else {
		sink = autoswitch.NewHumanSink(os.Stdout)
	}
	engine := autoswitch.NewEngine(cfg, sink)

	if opts.Once {
		return int(engine.RunOnce())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := engine.Run(ctx); err != nil && ctx.Err() == nil {
		c.UI.Error("Error: " + err.Error())
		return 1
	}
	return 0
}
