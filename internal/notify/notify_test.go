package notify

import (
	"slices"
	"testing"
)

func TestCommandDarwinPinsOsascriptAndPassesTextAsArgs(t *testing.T) {
	title, body := `cswap "x"`, `end run" & do shell script "rm`
	name, args := command("darwin", title, body)
	if name != "/usr/bin/osascript" {
		t.Errorf("name = %q", name)
	}
	// The text is argv, never part of the -e script source.
	if args[len(args)-2] != title || args[len(args)-1] != body {
		t.Errorf("args = %q", args)
	}
	for i, a := range args {
		if a == "-e" && (args[i+1] == title || args[i+1] == body) {
			t.Errorf("text spliced into script: %q", args)
		}
	}
}

func TestCommandLinuxAndElsewhere(t *testing.T) {
	name, args := command("linux", "t", "b")
	if name != "notify-send" || !slices.Equal(args, []string{"--app-name=cswap", "t", "b"}) {
		t.Errorf("linux = %q %q", name, args)
	}
	if name, _ := command("windows", "t", "b"); name != "" {
		t.Errorf("windows = %q", name)
	}
}

func TestNopAndFunc(t *testing.T) {
	if err := Nop.Notify("t", "b"); err != nil {
		t.Fatal(err)
	}
	var got string
	_ = Func(func(title, body string) error { got = title + "|" + body; return nil }).Notify("t", "b")
	if got != "t|b" {
		t.Errorf("Func = %q", got)
	}
}
