package claudecfg

import (
	"encoding/json"
	"os"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// ActiveModel returns the model selector Claude Code is currently configured
// to use — an alias ("fable", "opusplan"), a full ID ("claude-fable-5"), or a
// suffixed variant ("claude-fable-5[1m]") — from $ANTHROPIC_MODEL first, then
// the "model" key in the user-level settings.json. Empty when no model is
// pinned (Claude Code's default); project-level settings overrides are
// deliberately ignored, since account switching is machine-wide.
//
// The selector is returned verbatim: per-model usage windows are matched
// against it by name containment (see usage.RelevantWindows), so any model
// family works without a mapping table here.
func ActiveModel() string {
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		return m
	}
	raw, err := os.ReadFile(paths.ClaudeSettingsPath())
	if err != nil {
		return ""
	}
	var cfg struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	return cfg.Model
}
