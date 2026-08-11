package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blairham/go-claude-swap/internal/account"
)

const unitName = "cswap-auto.service"

func unitPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", unitName)
}

// RenderUnit produces the systemd user unit. Restart=always covers crashes;
// enabling it for default.target starts it at login.
func RenderUnit(exe string, extraArgs []string) string {
	cmd := exe + " auto"
	if len(extraArgs) > 0 {
		cmd += " " + strings.Join(extraArgs, " ")
	}
	return `[Unit]
Description=cswap auto-switch loop (Claude Code account rotation)
After=network-online.target

[Service]
ExecStart=` + cmd + `
Restart=always
RestartSec=30

[Install]
WantedBy=default.target
`
}

func installSystemd(exe string, extraArgs []string) (string, error) {
	path := unitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := account.WriteFileAtomic(path, []byte(RenderUnit(exe, extraArgs)), 0o644); err != nil {
		return "", err
	}
	for _, args := range [][]string{
		{"daemon-reload"},
		{"enable", "--now", unitName},
	} {
		if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
			return "", fmt.Errorf("systemctl --user %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
		}
	}
	msg := fmt.Sprintf("Installed systemd user unit %s\n  unit: %s\nIt starts at login and restarts on crash.", unitName, path)
	msg += "\nTo also run before you log in (headless machines): loginctl enable-linger $USER"
	return msg, nil
}

func uninstallSystemd() (string, error) {
	_ = exec.Command("systemctl", "--user", "disable", "--now", unitName).Run()
	if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return "Uninstalled systemd user unit " + unitName, nil
}

func statusSystemd() (string, error) {
	if _, err := os.Stat(unitPath()); err != nil {
		return "not installed", nil
	}
	out, _ := exec.Command("systemctl", "--user", "is-active", unitName).Output()
	state := strings.TrimSpace(string(out))
	if state == "" {
		state = "unknown"
	}
	return state + " (journalctl --user -u " + unitName + " for logs)", nil
}
