package claudecfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelWindowName(t *testing.T) {
	cases := map[string]string{
		"fable":              "Fable",
		"claude-fable-5":     "Fable",
		"claude-fable-5[1m]": "Fable",
		"Fable":              "Fable",
		"opus":               "Opus",
		"opusplan":           "Opus",
		"claude-opus-5":      "Opus",
		"claude-sonnet-4-6":  "Sonnet",
		"claude-haiku-4-5":   "Haiku",
		"":                   "",
		"claude-mythos-5":    "",
		"gpt-4o":             "",
	}
	for in, want := range cases {
		if got := ModelWindowName(in); got != want {
			t.Errorf("ModelWindowName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActiveModelWindow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_MODEL", "")

	if got := ActiveModelWindow(); got != "" {
		t.Fatalf("no settings file: got %q, want empty", got)
	}

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"model": "claude-fable-5[1m]", "theme": "auto"}`)
	if got := ActiveModelWindow(); got != "Fable" {
		t.Fatalf("settings model: got %q, want Fable", got)
	}

	write(`{"theme": "auto"}`)
	if got := ActiveModelWindow(); got != "" {
		t.Fatalf("model unset: got %q, want empty", got)
	}

	write(`{not json`)
	if got := ActiveModelWindow(); got != "" {
		t.Fatalf("corrupt settings: got %q, want empty", got)
	}

	// Env override wins over the settings file.
	write(`{"model": "claude-fable-5"}`)
	t.Setenv("ANTHROPIC_MODEL", "claude-opus-5")
	if got := ActiveModelWindow(); got != "Opus" {
		t.Fatalf("env override: got %q, want Opus", got)
	}
}
