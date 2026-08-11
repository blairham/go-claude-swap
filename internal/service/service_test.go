package service

import (
	"strings"
	"testing"
)

func TestRenderPlist(t *testing.T) {
	got := RenderPlist("/usr/local/bin/cswap", []string{"--threshold", "85"})
	for _, want := range []string{
		"<string>" + Label + "</string>",
		"<string>/usr/local/bin/cswap</string>",
		"<string>auto</string>",
		"<string>--threshold</string>",
		"<string>85</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q:\n%s", want, got)
		}
	}
}

func TestRenderPlistEscapesXML(t *testing.T) {
	got := RenderPlist(`/path/with <&> chars/cswap`, nil)
	if strings.Contains(got, "with <&> chars") {
		t.Error("XML special characters must be escaped")
	}
	if !strings.Contains(got, "with &lt;&amp;&gt; chars") {
		t.Errorf("expected escaped path in:\n%s", got)
	}
}

func TestRenderUnit(t *testing.T) {
	got := RenderUnit("/usr/local/bin/cswap", []string{"--json"})
	for _, want := range []string{
		"ExecStart=/usr/local/bin/cswap auto --json",
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unit missing %q:\n%s", want, got)
		}
	}
}
