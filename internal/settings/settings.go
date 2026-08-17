// Package settings manages cswap's settings.json: a small registry of typed,
// bounded keys. Loading is forgiving (bad values clamp or fall back to
// defaults); `config set` validates strictly; unknown keys survive
// round-trips.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/blairham/go-claude-swap/internal/account"
	"github.com/blairham/go-claude-swap/internal/claudecfg"
	"github.com/blairham/go-claude-swap/internal/paths"
)

// SchemaVersion of settings.json.
const SchemaVersion = 1

// Kind is a setting's value type.
type Kind int

// Kinds.
const (
	KindFloat Kind = iota
	KindInt
	KindBool
	KindString
	KindChoice
)

// Spec describes one registered setting.
type Spec struct {
	Key     string // dotted, e.g. "autoswitch.threshold"
	Kind    Kind
	Lo, Hi  float64
	Choices []string
	Default any // nil for unset-by-default strings
	Help    string
}

// Registry lists every setting in display order.
var Registry = []Spec{
	{
		Key: "autoswitch.threshold", Kind: KindFloat, Lo: 50, Hi: 99.9, Default: 90.0,
		Help: "Switch when the binding 5h/7d window reaches this pct",
	},
	{
		Key: "autoswitch.intervalSeconds", Kind: KindFloat, Lo: 15, Hi: 3600, Default: 60.0,
		Help: "Poll interval for the cswap auto loop, in seconds",
	},
	{
		Key: "autoswitch.cooldownSeconds", Kind: KindFloat, Lo: 0, Hi: 86400, Default: 300.0,
		Help: "Minimum seconds between proactive switches",
	},
	{
		Key: "autoswitch.hysteresisPct", Kind: KindFloat, Lo: 0, Hi: 50, Default: 10.0,
		Help: "A target must beat the active account by this many pct",
	},
	{
		Key: "autoswitch.strategy", Kind: KindChoice, Choices: []string{"best", "consume-first"}, Default: "best",
		Help: "How auto-switch picks the target account",
	},
	{
		Key: "autoswitch.includeApiKeyAccounts", Kind: KindBool, Default: false,
		Help: "Allow rotating onto managed API-key accounts (bill per token)",
	},
	{
		Key: "autoswitch.unhealthyTicks", Kind: KindInt, Lo: 1, Hi: 100, Default: 3,
		Help: "Consecutive failed polls before an account is unhealthy",
	},
	{
		Key: "autoswitch.model", Kind: KindString, Default: "auto",
		Help: "Model weekly limits to also switch on: auto (follow Claude Code's model), names (e.g. Fable,Opus), all, or none",
	},
	{
		Key: "ui.theme", Kind: KindChoice, Choices: []string{"dark", "light", "auto"}, Default: "auto",
		Help: "Color theme; auto follows the terminal background",
	},
}

// Settings holds effective values plus which keys were literally present.
type Settings struct {
	values map[string]any
	isSet  map[string]bool
}

// Get returns the effective value for a registered key.
func (s *Settings) Get(key string) any { return s.values[key] }

// IsSet reports whether the key is literally present in the file.
func (s *Settings) IsSet(key string) bool { return s.isSet[key] }

// Float returns a float setting.
func (s *Settings) Float(key string) float64 {
	v, _ := s.values[key].(float64)
	return v
}

// Int returns an int setting.
func (s *Settings) Int(key string) int {
	switch v := s.values[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// Bool returns a bool setting.
func (s *Settings) Bool(key string) bool {
	v, _ := s.values[key].(bool)
	return v
}

// String returns a string setting ("" when unset).
func (s *Settings) String(key string) string {
	v, _ := s.values[key].(string)
	return v
}

// Set overrides a value in memory (CLI flag overlay); it is re-clamped.
func (s *Settings) Set(key string, value any) {
	if spec := lookup(key); spec != nil {
		if v, ok := coerceForgiving(spec, value); ok {
			s.values[key] = v
		}
	}
}

// Models parses autoswitch.model: comma-split, trimmed, case-insensitively
// deduped (first spelling wins). Empty when unset. Sentinels ("auto",
// "none") pass through unexpanded — see ResolveModelNames.
func (s *Settings) Models() []string {
	return ParseModelNames(s.String("autoswitch.model"))
}

// ResolvedModels is Models with sentinels expanded via ResolveModelNames.
func (s *Settings) ResolvedModels() []string {
	return ResolveModelNames(s.Models())
}

// ResolveModelNames expands sentinels in a parsed model list: "auto" becomes
// the model Claude Code is currently configured to use (dropped when that is
// undetectable or has no per-model window), and "none" is discarded, so
// "none" alone disables model windows. Real names pass through. Resolve at
// decision time, not once at startup — the model can change while a service
// runs.
func ResolveModelNames(models []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[strings.ToLower(name)] {
			return
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}
	for _, m := range models {
		switch strings.ToLower(m) {
		case "none":
		case "auto":
			add(claudecfg.ActiveModelWindow())
		default:
			add(m)
		}
	}
	return out
}

// ParseModelNames splits a comma-separated model list.
func ParseModelNames(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for part := range strings.SplitSeq(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" || seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		out = append(out, p)
	}
	return out
}

func lookup(key string) *Spec {
	for i := range Registry {
		if Registry[i].Key == key {
			return &Registry[i]
		}
	}
	return nil
}

// Load reads settings.json forgivingly: any corruption yields defaults.
func Load() *Settings {
	s := &Settings{values: map[string]any{}, isSet: map[string]bool{}}
	for _, spec := range Registry {
		s.values[spec.Key] = spec.Default
	}
	raw, err := os.ReadFile(paths.SettingsPath())
	if err != nil {
		return s
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return s
	}
	for _, spec := range Registry {
		section, field, _ := strings.Cut(spec.Key, ".")
		sec, _ := doc[section].(map[string]any)
		if sec == nil {
			continue
		}
		rawVal, present := sec[field]
		if !present {
			continue
		}
		s.isSet[spec.Key] = true
		if v, ok := coerceForgiving(&spec, rawVal); ok {
			s.values[spec.Key] = v
		}
	}
	return s
}

// coerceForgiving applies load-time coercion: clamp numerics, default on
// wrong types.
func coerceForgiving(spec *Spec, raw any) (any, bool) {
	switch spec.Kind {
	case KindFloat, KindInt:
		f, ok := raw.(float64)
		if !ok {
			return nil, false
		}
		f = math.Min(math.Max(f, spec.Lo), spec.Hi)
		if spec.Kind == KindInt {
			return int(f), true
		}
		return f, true
	case KindBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, false
		}
		return b, true
	case KindString:
		v, ok := raw.(string)
		if !ok || v == "" {
			return nil, false
		}
		return v, true
	case KindChoice:
		v, ok := raw.(string)
		if !ok || !slices.Contains(spec.Choices, v) {
			return nil, false
		}
		return v, true
	}
	return nil, false
}

// ParseStrict validates a user-supplied string for `config set`: errors, no
// clamping.
func ParseStrict(key, value string) (any, error) {
	spec := lookup(key)
	if spec == nil {
		return nil, fmt.Errorf("unknown setting %q", key)
	}
	switch spec.Kind {
	case KindFloat, KindInt:
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("%s must be a number", key)
		}
		if f < spec.Lo || f > spec.Hi {
			return nil, fmt.Errorf("%s must be between %s and %s", key, trimFloat(spec.Lo), trimFloat(spec.Hi))
		}
		if spec.Kind == KindInt {
			return int(f), nil
		}
		return f, nil
	case KindBool:
		switch strings.ToLower(value) {
		case "true", "1", "yes":
			return true, nil
		case "false", "0", "no":
			return false, nil
		}
		return nil, fmt.Errorf("%s must be true or false", key)
	case KindString:
		v := strings.TrimSpace(value)
		if v == "" {
			return nil, fmt.Errorf("%s cannot be empty", key)
		}
		return v, nil
	case KindChoice:
		if slices.Contains(spec.Choices, value) {
			return value, nil
		}
		return nil, fmt.Errorf("%s must be one of: %s", key, strings.Join(spec.Choices, ", "))
	}
	return nil, fmt.Errorf("unknown setting %q", key)
}

// SetKey writes one key to settings.json (read-modify-write preserving
// everything else). A corrupt existing file is an error, never replaced.
func SetKey(key string, value any) error {
	doc, err := readDocForWrite()
	if err != nil {
		return err
	}
	section, field, _ := strings.Cut(key, ".")
	sec, _ := doc[section].(map[string]any)
	if sec == nil {
		sec = map[string]any{}
	}
	sec[field] = value
	doc[section] = sec
	return writeDoc(doc)
}

// UnsetKey removes a key; returns false when it was not set.
func UnsetKey(key string) (bool, error) {
	doc, err := readDocForWrite()
	if err != nil {
		return false, err
	}
	section, field, _ := strings.Cut(key, ".")
	sec, _ := doc[section].(map[string]any)
	if sec == nil {
		return false, nil
	}
	if _, present := sec[field]; !present {
		return false, nil
	}
	delete(sec, field)
	if len(sec) == 0 {
		delete(doc, section)
	} else {
		doc[section] = sec
	}
	return true, writeDoc(doc)
}

func readDocForWrite() (map[string]any, error) {
	raw, err := os.ReadFile(paths.SettingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if jerr := json.Unmarshal(raw, &doc); jerr != nil {
		return nil, fmt.Errorf("%s is corrupt — fix or delete it: %w", paths.SettingsPath(), jerr)
	}
	return doc, nil
}

func writeDoc(doc map[string]any) error {
	doc["schemaVersion"] = SchemaVersion
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return account.WriteFileAtomic(paths.SettingsPath(), data, 0o600)
}

// FormatValue renders a value for display: (none), true/false, integral
// floats without the decimal.
func FormatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "(none)"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return trimFloat(t)
	case int:
		return strconv.Itoa(t)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', 10, 64)
}
