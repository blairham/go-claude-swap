package cmd

import (
	"fmt"

	"github.com/hashicorp/cli"

	"github.com/blairham/go-claude-swap/internal/paths"
	"github.com/blairham/go-claude-swap/internal/settings"
)

// ConfigCommand manages cswap settings.
type ConfigCommand struct {
	UI cli.Ui
}

// ConfigFlags for cswap config.
type ConfigFlags struct {
	JSON bool `long:"json" description:"Emit machine-readable JSON (list/get only)"`
}

// Help text.
func (c *ConfigCommand) Help() string {
	return `Usage: cswap config [list|get <key>|set <key> <value>|unset <key>|path]

Manage cswap settings (settings.json in the backup root). With no action,
lists every setting with its effective value.

Examples:
  cswap config
  cswap config set autoswitch.threshold 85
  cswap config unset autoswitch.model
  cswap config path
`
}

// Synopsis line.
func (c *ConfigCommand) Synopsis() string {
	return "Manage cswap settings"
}

// Run executes the command.
func (c *ConfigCommand) Run(args []string) int {
	var opts ConfigFlags
	remaining, stop, code := parseFlags(c.UI, c.Help(), &opts, args)
	if stop {
		return code
	}

	action := "list"
	if len(remaining) > 0 {
		action = remaining[0]
	}
	s := settings.Load()

	switch action {
	case "list":
		if opts.JSON {
			rows := []map[string]any{}
			for _, spec := range settings.Registry {
				rows = append(rows, map[string]any{
					"key":   spec.Key,
					"value": s.Get(spec.Key),
					"isSet": s.IsSet(spec.Key),
				})
			}
			return printJSON(c.UI, map[string]any{"path": paths.SettingsPath(), "settings": rows})
		}
		width := 0
		for _, spec := range settings.Registry {
			if len(spec.Key) > width {
				width = len(spec.Key)
			}
		}
		for _, spec := range settings.Registry {
			suffix := ""
			if !s.IsSet(spec.Key) {
				suffix = "  (default)"
			}
			c.UI.Output(fmt.Sprintf("%-*s  %s%s", width, spec.Key, settings.FormatValue(s.Get(spec.Key)), suffix))
		}
		return 0

	case "get":
		if len(remaining) != 2 {
			c.UI.Error("Usage: cswap config get <key>")
			return 1
		}
		key := remaining[1]
		if opts.JSON {
			return printJSON(c.UI, map[string]any{"key": key, "value": s.Get(key), "isSet": s.IsSet(key)})
		}
		c.UI.Output(settings.FormatValue(s.Get(key)))
		return 0

	case "set":
		if len(remaining) != 3 {
			c.UI.Error("Usage: cswap config set <key> <value>")
			return 1
		}
		key, raw := remaining[1], remaining[2]
		value, err := settings.ParseStrict(key, raw)
		if err != nil {
			c.UI.Error("Error: " + err.Error())
			return 1
		}
		if err := settings.SetKey(key, value); err != nil {
			c.UI.Error("Error: " + err.Error())
			return 1
		}
		c.UI.Output(fmt.Sprintf("%s = %s", key, settings.FormatValue(value)))
		return 0

	case "unset":
		if len(remaining) != 2 {
			c.UI.Error("Usage: cswap config unset <key>")
			return 1
		}
		key := remaining[1]
		removed, err := settings.UnsetKey(key)
		if err != nil {
			c.UI.Error("Error: " + err.Error())
			return 1
		}
		if !removed {
			c.UI.Error(fmt.Sprintf("%s is not set; nothing to do", key))
			return 0
		}
		fresh := settings.Load()
		c.UI.Output(fmt.Sprintf("%s unset (default: %s)", key, settings.FormatValue(fresh.Get(key))))
		return 0

	case "path":
		c.UI.Output(paths.SettingsPath())
		return 0

	default:
		c.UI.Error(c.Help())
		return 1
	}
}
