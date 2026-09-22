package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Uses actual workmux and an isolated tmux server; agents are inert Go processes.
func TestRealWorkmuxSmoke(t *testing.T) {
	if os.Getenv("WMM_SMOKE") != "1" {
		t.Skip("opt in with make smoke")
	}
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = exec.LookPath("workmux"); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	bin := filepath.Join(f.root, "bin")
	if err = os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(bin, "claude")
	cmd := exec.Command("go", "build", "-o", agent, "./testdata/helper")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test helper: %v\n%s", err, data)
	}
	if err = os.Symlink(agent, filepath.Join(bin, "tmux")); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("wmm-smoke-%d", os.Getpid())
	t.Setenv("WMM_TEST_SOCKET", socket)
	t.Setenv("WMM_TEST_REAL_TMUX", realTmux)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(f.root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(f.root, "cache"))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	if err = os.MkdirAll(filepath.Join(f.root, "config", "workmux"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.root, "config", "workmux", "config.yaml"), []byte("nerdfont: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command(realTmux, "-L", socket, "kill-server").Run() })
	f.a.run = execute
	f.a.lookup = nil
	f.mustCLI("start", "feat/shared", "api", "web", "--prompt", "Smoke test only")
	m := f.manifest()
	time.Sleep(300 * time.Millisecond)
	if active := f.a.windows(m.Session); len(active) != 1 || active["build"] == "" {
		t.Fatal(active)
	}
	// Keep the isolated server alive during the transition to reviews.
	if _, err = execute("", "tmux", "new-window", "-d", "-t", m.Session+":", "-n", "control"); err != nil {
		t.Fatal(err)
	}
	if _, err = execute("", "tmux", "kill-window", "-t", f.a.windows(m.Session)["build"]); err != nil {
		t.Fatal(err)
	}
	f.mustCLI("review", "feat/shared", "--prepare-pr")
	time.Sleep(300 * time.Millisecond)
	active := f.a.windows(m.Session)
	if active["wmm-review-api"] == "" || active["wmm-review-web"] == "" {
		t.Fatal(active)
	}
	f.mustCLI("review", "feat/shared")
	if len(f.a.windows(m.Session)) != 3 {
		t.Fatal("duplicate review windows")
	}
	f.mustCLI("status", "feat/shared", "--fields", "repo,state,commits,path")
	// Verify the real agent processes run in their individual worktrees.
	out, err := execute("", "tmux", "list-panes", "-s", "-t", m.Session, "-F", "#{pane_current_path}")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Repos {
		if !bytes.Contains([]byte(out), []byte(r.Path)) {
			t.Fatalf("missing pane for %s: %s", r.Alias, out)
		}
	}
	t.Log("real headless provisioning, shared builder, independent reviewers, and idempotent reopening passed")
}
