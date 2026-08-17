package claudecfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActiveModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_MODEL", "")

	if got := ActiveModel(); got != "" {
		t.Fatalf("no settings file: got %q, want empty", got)
	}

	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"model": "claude-fable-5[1m]", "theme": "auto"}`)
	if got := ActiveModel(); got != "claude-fable-5[1m]" {
		t.Fatalf("settings model: got %q, want claude-fable-5[1m]", got)
	}

	write(`{"theme": "auto"}`)
	if got := ActiveModel(); got != "" {
		t.Fatalf("model unset: got %q, want empty", got)
	}

	write(`{not json`)
	if got := ActiveModel(); got != "" {
		t.Fatalf("corrupt settings: got %q, want empty", got)
	}

	// Env override wins over the settings file.
	write(`{"model": "claude-fable-5"}`)
	t.Setenv("ANTHROPIC_MODEL", "claude-opus-5")
	if got := ActiveModel(); got != "claude-opus-5" {
		t.Fatalf("env override: got %q, want claude-opus-5", got)
	}
}
