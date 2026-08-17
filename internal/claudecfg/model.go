package claudecfg

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/blairham/go-claude-swap/internal/paths"
)

// modelFamilies maps model-selector substrings to the display names the
// usage endpoint gives their per-model weekly windows (e.g. a limits entry
// scoped to "Fable"). Order matters only for pathological selectors that
// name two families; first match wins.
var modelFamilies = []struct{ token, window string }{
	{"fable", "Fable"},
	{"opus", "Opus"},
	{"sonnet", "Sonnet"},
	{"haiku", "Haiku"},
}

// ActiveModelWindow resolves the usage-window display name for the model
// Claude Code is currently configured to use: $ANTHROPIC_MODEL first, then
// the "model" key in the user-level settings.json. Empty when no model is
// pinned (Claude Code's default) or the selector names no known family —
// project-level settings overrides are deliberately ignored, since account
// switching is machine-wide.
func ActiveModelWindow() string {
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		return ModelWindowName(m)
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
	return ModelWindowName(cfg.Model)
}

// ModelWindowName maps a model selector — an alias ("fable", "opusplan"), a
// full ID ("claude-fable-5"), or a suffixed variant ("claude-fable-5[1m]")
// — to its per-model usage-window display name; "" for unknown families.
func ModelWindowName(model string) string {
	m := strings.ToLower(model)
	for _, fam := range modelFamilies {
		if strings.Contains(m, fam.token) {
			return fam.window
		}
	}
	return ""
}
