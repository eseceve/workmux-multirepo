// Test-only executables for an isolated workmux/tmux smoke test.
package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	if filepath.Base(os.Args[0]) != "tmux" {
		cwd, _ := os.Getwd()
		if file, err := os.CreateTemp(os.Getenv("WMM_TEST_AGENT_LOG"), "invocation-"); err == nil {
			json.NewEncoder(file).Encode(struct {
				Args []string
				Cwd  string
			}{os.Args, cwd})
			file.Close()
		}
		time.Sleep(2 * time.Minute)
		return
	}
	args := append([]string{"-L", os.Getenv("WMM_TEST_SOCKET")}, os.Args[1:]...)
	cmd := exec.Command(os.Getenv("WMM_TEST_REAL_TMUX"), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
