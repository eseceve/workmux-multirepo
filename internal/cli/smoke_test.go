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
	checkWindow("build", prIcon+" "+m.Branch, 2)
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
	checkWindow("build", prIcon+" "+m.Branch, 2)
	// Renaming must not disable lifecycle guards.
	if _, err = execute("", "tmux", "rename-window", "-t", f.a.windows(m.Session)["build"], "renamed"); err != nil {
		t.Fatal(err)
	}
	f.mustCLI("start", "feat/shared", "api", "web")
	checkWindow("build", prIcon+" "+m.Branch, 2)
	index := func(window string) string {
		t.Helper()
		out, err := execute("", "tmux", "display-message", "-p", "-t", window, "#{window_index}")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	builder := f.a.windows(m.Session)["build"]
	position := index(builder)
	pane, err := execute("", "tmux", "display-message", "-p", "-t", builder, "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_PANE", strings.TrimSpace(pane))
	f.mustCLI("review", "feat/shared")
	checkAgent("codex", m.Repos[0].Path)
	checkAgent("gemini", m.Repos[1].Path)
	// Reviewers replace the implementation window in place and close the builder.
	review := f.a.windows(m.Session)["review"]
	if review == "" || f.a.windows(m.Session)["build"] != "" || index(review) != position {
		t.Fatal("review did not replace the implementation window", f.a.windows(m.Session))
	}
	checkWindow("review", reviewIcon+" "+m.Branch, 5)
	control, err := execute("", "tmux", "display-message", "-p", "-t", "current:control", "#{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_PANE", strings.TrimSpace(control))
	f.mustCLI("review", "feat/shared")
	if len(f.a.windows(m.Session)) != 2 {
		t.Fatal("duplicate review windows")
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
	// Starting again brings the builder back in place of the reviewers.
	f.mustCLI("start", "feat/shared", "api", "web")
	builder = f.a.windows(m.Session)["build"]
	if builder == "" || f.a.windows(m.Session)["review"] != "" || index(builder) != position {
		t.Fatal("builder did not replace the review window", f.a.windows(m.Session))
	}
	checkWindow("build", prIcon+" "+m.Branch, 2)
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

	// Simulate losing the receipt after workmux has created a real worktree.
	// Retry must remove only that incomplete worktree and run setup again.
	addCalls := 0
	f.a.run = func(cwd string, args ...string) (string, error) {
		out, err := execute(cwd, args...)
		if len(args) > 2 && args[0] == "workmux" && args[1] == "add" && args[2] != "--help" {
			addCalls++
			if err == nil && addCalls == 2 {
				return "", fmt.Errorf("simulated lost provisioning receipt")
			}
		}
		return out, err
	}
	if code, out := f.cli("start", "feat/retry", "api", "web", "--no-open"); code != 1 {
		t.Fatalf("expected injected failure: %d %s", code, out)
	}
	retry, err := load(f.config(), "feat/retry")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Repos[1].Path == "" || retry.Repos[1].Ready {
		t.Fatal("missing record of incomplete worktree")
	}
	marker := filepath.Join(retry.Repos[0].Path, "keep.txt")
	write(marker, "preserved")
	f.mustCLI("start", "feat/retry", "api", "web", "--no-open")
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserved" || addCalls != 3 {
		t.Fatal("retry did not preserve completed work and recreate only incomplete worktree", err, addCalls)
	}
	t.Log("real workmux recovery after lost receipt passed")
	f.a.run = execute
	f.mustCLI("remove", "feat/retry", "--force", "--keep-branch")
	for _, r := range retry.Repos {
		gitTest(t, r.Source, "rev-parse", "--verify", "refs/heads/feat/retry")
		if exists(r.Path) {
			t.Fatal("worktree remains after group removal", r.Path)
		}
	}
	f.mustCLI("remove", "feat/shared")
	if exists(dir) {
		t.Fatal("workspace remains after removal")
	}
	if windows := f.a.windows(m.Session); len(windows) != 1 || windows["control"] == "" {
		t.Fatal("removal did not preserve only the unrelated control window", windows)
	}
	f.mustCLI("remove", "feat/shared")
	t.Log("real group removal, branch retention, and unrelated window preservation passed")

	// Debug from the root directory, then start one repository in the same place.
	f.mustCLI("debug", "fix/debugged")
	debugWindow := f.a.windows("current")["debug"]
	debugPosition := index(debugWindow)
	checkWindow("debug", debugIcon+" fix/debugged", 1)
	if path, _ := execute("", "tmux", "display-message", "-p", "-t", debugWindow, "#{pane_current_path}"); strings.TrimSpace(path) != f.root {
		t.Fatalf("debug window opened in %q", path)
	}
	f.mustCLI("start", "fix/debugged", "api")
	debugged, err := load(f.config(), "fix/debugged")
	if err != nil {
		t.Fatal(err)
	}
	checkAgent("codex", debugged.Repos[0].Path)
	builder = f.a.windows(debugged.Session)["build"]
	if builder == "" || f.a.windows(debugged.Session)["debug"] != "" || index(builder) != debugPosition {
		t.Fatal("builder did not replace the debug window", f.a.windows(debugged.Session))
	}
	name, _ := execute("", "tmux", "display-message", "-p", "-t", builder, "#{window_name}")
	if strings.TrimSpace(name) != prIcon+" api:fix/debugged" {
		t.Fatalf("single-repository builder named %q", name)
	}
	f.mustCLI("remove", "fix/debugged", "--force")
	t.Log("debug to single-repository start in place passed")

	// Exercise the installed CLI process from a window it must close itself.
	// Closing that terminal must not interrupt final manifest cleanup.
	cliBin := filepath.Join(bin, "wmm")
	build := exec.Command("go", "build", "-o", cliBin, "../../cmd/wmm")
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, data)
	}
	f.mustCLI("start", "feat/self-remove", "api", "web", "--no-open")
	selfDir := workspace(f.config(), "feat/self-remove")
	self, err := load(f.config(), "feat/self-remove")
	if err != nil {
		t.Fatal(err)
	}
	self.Session = "current"
	if err := save(f.config(), self); err != nil {
		t.Fatal(err)
	}
	gate, log := filepath.Join(bin, "remove-go"), filepath.Join(bin, "remove.log")
	script := filepath.Join(bin, "remove.sh")
	write(script, "while [ ! -f "+quote(gate)+" ]; do sleep 0.05; done\nexec "+quote(cliBin)+" --config "+quote(filepath.Join(f.root, "wmm.toml"))+" remove feat/self-remove > "+quote(log)+" 2>&1\n")
	window, err := execute("", "tmux", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "current", "-n", "self-remove", "-c", selfDir, "sh "+quote(script))
	if err != nil {
		t.Fatal(err)
	}
	window = strings.TrimSpace(window)
	if _, err := execute("", "tmux", "set-window-option", "-t", window, "@wmm_workspace", selfDir); err != nil {
		t.Fatal(err)
	}
	write(gate, "go")
	deadline := time.Now().Add(10 * time.Second)
	for exists(selfDir) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if exists(selfDir) {
		data, _ := os.ReadFile(log)
		t.Fatalf("removal from own terminal did not finish: %s", data)
	}
	t.Log("CLI removal from its own terminal completed")
}
