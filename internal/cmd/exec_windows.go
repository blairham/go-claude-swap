//go:build windows

package cmd

import (
	"errors"
	"os"
	"os/exec"
)

// execClaude runs claude as a child and mirrors its exit code: exec on
// Windows detaches from the console.
func execClaude(bin string, args, env []string) (int, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}
