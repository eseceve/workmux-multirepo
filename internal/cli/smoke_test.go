package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	agent := filepath.Join(bin, "codex")
	cmd := exec.Command("go", "build", "-o", agent, "./testdata/helper")
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test helper: %v\n%s", err, data)
	}
	for _, name := range []string{"tmux", "opencode", "gemini"} {
		if err = os.Symlink(agent, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	socket := fmt.Sprintf("wmm-smoke-%d", os.Getpid())
	t.Setenv("WMM_TEST_SOCKET", socket)
	t.Setenv("WMM_TEST_REAL_TMUX", realTmux)
	t.Setenv("WMM_TEST_AGENT_LOG", bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(f.root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(f.root, "cache"))
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(f.root, "config", "workmux", "config.yaml"), "nerdfont: false\nagent: "+quote(agent)+"\nwindow_prefix: custom-\npanes:\n  - command: <agent>\n  - split: horizontal\n")
	write(filepath.Join(f.repos["web"], ".workmux.yaml"), "agent: "+quote(filepath.Join(bin, "gemini"))+"\nwindow_prefix: web-\npanes:\n  - command: <agent>\n  - split: horizontal\n  - split: vertical\n")
	t.Cleanup(func() { exec.Command(realTmux, "-L", socket, "kill-server").Run() })
	f.a.run, f.a.lookup = execute, nil
	if _, err = execute("", "tmux", "new-session", "-d", "-s", "current", "-n", "control"); err != nil {
		t.Fatal(err)
	}
	tmuxEnv, err := execute("", "tmux", "display-message", "-p", "-t", "current", "#{socket_path},#{pid},#{session_id}")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", strings.TrimSpace(tmuxEnv))

	// Fresh workmux installs default to shell/clear panes, which cannot receive
	// wmm's prompt. Configuring an agent pane must let the same start recover.
	globalConfig := filepath.Join(f.root, "config", "workmux", "config.yaml")
	configured, err := os.ReadFile(globalConfig)
	if err != nil {
		t.Fatal(err)
	}
	write(globalConfig, "nerdfont: false\n")
	code, _ := f.cli("start", "feat/shared", "api", "web", "--prompt", "Smoke test only")
	if code != 1 || !strings.Contains(f.diagnostic.String(), "no pane is configured to run the agent") {
		t.Fatalf("expected missing agent pane: code=%d diagnostics=%s", code, f.diagnostic.String())
	}
	t.Log("reproduced: prompt provided, but no pane is configured to run the agent")
	write(globalConfig, string(configured))
	f.mustCLI("start", "feat/shared", "api", "web", "--prompt", "Smoke test only")
	m := f.manifest()
	if m.Session != "current" {
		t.Fatalf("unexpected session %s", m.Session)
	}
	dir := workspace(f.config(), m.Branch)
	checkAgent := func(name, cwd string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			entries, _ := filepath.Glob(filepath.Join(bin, "invocation-*"))
			for _, entry := range entries {
				data, _ := os.ReadFile(entry)
				var call struct {
					Args []string
					Cwd  string
				}
				if json.Unmarshal(data, &call) == nil && len(call.Args) >= 1 && filepath.Base(call.Args[0]) == name && call.Cwd == cwd {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		for _, id := range f.a.windows(m.Session) {
			out, _ := execute("", "tmux", "capture-pane", "-p", "-t", id)
			t.Log(out)
		}
		t.Fatalf("workmux did not launch %s in %s", name, cwd)
	}
	checkWindow := func(role, name string, panes int) {
		t.Helper()
		id := f.a.windows(m.Session)[role]
		names, err := execute("", "tmux", "display-message", "-p", "-t", id, "#{window_name}:#{window_panes}")
		if id == "" || err != nil || strings.TrimSpace(names) != fmt.Sprintf("%s:%d", name, panes) {
			t.Fatalf("window %s: %q %v", role, names, err)
		}
	}
	checkAgent("codex", dir)
	checkWindow("build", m.Branch, 2)
	f.mustCLI("start", "feat/shared", "api", "web")
	closeBuilder := func() {
		t.Helper()
		if _, err := execute("", "tmux", "kill-window", "-t", f.a.windows(m.Session)["build"]); err != nil {
			t.Fatal(err)
		}
	}
	closeBuilder()
	// An existing workspace workmux config overrides global settings unchanged.
	write(filepath.Join(f.root, ".workmux.yaml"), "agent: "+quote(filepath.Join(bin, "opencode"))+"\nwindow_prefix: shared-\n")
	f.mustCLI("start", "feat/shared", "api", "web")
	checkAgent("opencode", dir)
	checkWindow("build", m.Branch, 2)
	// Renaming must not disable lifecycle guards.
	if _, err = execute("", "tmux", "rename-window", "-t", f.a.windows(m.Session)["build"], "renamed"); err != nil {
		t.Fatal(err)
	}
	f.mustCLI("start", "feat/shared", "api", "web")
	checkWindow("build", m.Branch, 2)
	builder := f.a.windows(m.Session)["build"]
	pane, err := execute("", "tmux", "display-message", "-p", "-t", builder, "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_PANE", strings.TrimSpace(pane))
	f.mustCLI("review", "feat/shared")
	checkAgent("codex", m.Repos[0].Path)
	checkAgent("gemini", m.Repos[1].Path)
	checkWindow("review", m.Branch, 7)
	if f.a.windows(m.Session)["review"] != builder {
		t.Fatal("did not reuse builder")
	}
	f.mustCLI("review", "feat/shared")
	if len(f.a.windows(m.Session)) != 2 {
		t.Fatal("duplicate review windows")
	}
	if code, _ := f.cli("start", "feat/shared", "api", "web"); code != 1 {
		t.Fatal("reviewers bypassed guard")
	}
	// From a different window, create one separate review window while keeping
	// the implementation window and all of its processes alive.
	control, err := execute("", "tmux", "display-message", "-p", "-t", "current:control", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_PANE", strings.TrimSpace(control))
	if _, err = execute("", "tmux", "kill-window", "-t", builder); err != nil {
		t.Fatal(err)
	}
	f.mustCLI("start", "feat/shared", "api", "web")
	builder = f.a.windows(m.Session)["build"]
	f.mustCLI("review", "feat/shared")
	review := f.a.windows(m.Session)["review"]
	if review == "" || review == builder || f.a.windows(m.Session)["build"] != builder || len(f.a.windows(m.Session)) != 3 {
		t.Fatal("review from another window did not create a separate group")
	}
	checkWindow("review", "custom-review-api-"+digest(dir, 8), 5)
	f.mustCLI("review", "feat/shared")
	if len(f.a.windows(m.Session)) != 3 {
		t.Fatal("duplicate grouped reviews")
	}
	f.mustCLI("status", "feat/shared", "--fields", "repo,state,commits,path")
	out, err := execute("", "tmux", "list-panes", "-s", "-t", m.Session, "-F", "#{pane_current_path}")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Repos {
		if !bytes.Contains([]byte(out), []byte(r.Path)) {
			t.Fatalf("missing pane for %s: %s", r.Alias, out)
		}
	}
	sessions, err := execute("", "tmux", "list-sessions", "-F", "#{session_name}")
	if err != nil || strings.TrimSpace(sessions) != "current" {
		t.Fatalf("unexpected sessions: %q %v", sessions, err)
	}
	for _, name := range []string{"BRIEF.md", "build-prompt.md", "review-api-prompt.md", "review-web-prompt.md"} {
		if exists(filepath.Join(dir, name)) {
			t.Errorf("generated %s", name)
		}
	}
	t.Log("native global/project agents, pane layouts, prefixes, prompts, lifecycle guards, and idempotent reopening passed")
}
