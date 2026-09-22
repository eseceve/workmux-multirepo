// Test-only executables for an isolated workmux/tmux smoke test.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	if filepath.Base(os.Args[0]) == "claude" {
		time.Sleep(2 * time.Minute)
		return
	}
	args := append([]string{"-L", os.Getenv("WMM_TEST_SOCKET")}, os.Args[1:]...)
	cmd := exec.Command(os.Getenv("WMM_TEST_REAL_TMUX"), args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
