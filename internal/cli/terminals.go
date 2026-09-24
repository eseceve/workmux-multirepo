package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func sessionName(c *config, branch string) string { return "wmm-" + digest(c.Root+":"+branch, 12) }

// Roles are tmux metadata, independent of workmux's prefix or user renames.
func (a *app) windows(session string, owner ...string) map[string]string {
	text, err := a.run("", "tmux", "list-windows", "-t", "="+session, "-F", "#{window_id}\t#{window_name}\t#{@wmm_role}\t#{@wmm_workspace}")
	result := map[string]string{}
	if err != nil {
		return result
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		if len(owner) > 0 && (len(fields) < 4 || fields[3] != owner[0]) {
			continue
		}
		name := fields[1]
		if len(fields) > 2 && fields[2] != "" {
			name = fields[2]
		}
		// Recognize windows created by the previous release.
		name = strings.TrimPrefix(name, "wmm-review-")
		if strings.HasPrefix(fields[1], "wmm-review-") && (len(fields) < 3 || fields[2] == "") {
			name = "review-" + name
		}
		if _, duplicate := result[name]; duplicate {
			name += fields[0]
		}
		result[name] = fields[0]
	}
	return result
}

// Legacy dedicated sessions have no workspace metadata.
func (a *app) managedWindows(c *config, m *manifest) map[string]string {
	if m.Session == sessionName(c, m.Branch) {
		return a.windows(m.Session)
	}
	return a.windows(m.Session, workspace(c, m.Branch))
}

func (a *app) bindSession(c *config, m *manifest, allowActive bool) error {
	if os.Getenv("TMUX") == "" {
		return nil
	}
	current, err := a.tmuxContext("#{session_name}")
	if err != nil {
		return err
	}
	current = strings.TrimSpace(current)
	if current == "" {
		return fail("Cannot determine current tmux session.", "Run from a tmux pane and retry.")
	}
	if current == m.Session {
		return nil
	}
	if !allowActive && len(a.managedWindows(c, m)) > 0 {
		return fail("This change still has windows in session "+m.Session, "Close those windows before reopening in the current session.")
	}
	m.Session = current
	return save(c, m)
}

func (a *app) prerequisites(c *config) error {
	for _, name := range []string{"git", "workmux", "tmux"} {
		if _, err := a.findCommand(name); err != nil {
			return fail("Executable unavailable: "+name, "Install Git, tmux, and workmux, then retry.")
		}
	}
	text, err := a.command(c.Root, "workmux", "add", "--help")
	if err != nil {
		return err
	}
	if !strings.Contains(text, "--headless") || !strings.Contains(text, "--json") {
		return fail("Installed workmux lacks headless provisioning.", "Update workmux; wmm handles the provisioning flags internally.")
	}
	return nil
}
func isReview(role string) bool { return role == "review" || strings.HasPrefix(role, "review-") }

// Workmux requires a Git worktree to launch an agent. This empty local repository
// gives the shared workspace that context without adopting any source checkout.
func (a *app) coordinator(dir string) error {
	if !exists(filepath.Join(dir, ".git")) {
		if _, err := a.git(dir, "init", "--initial-branch=main", "."); err != nil {
			return err
		}
	}
	if _, err := a.tryGit(dir, "rev-parse", "--verify", "HEAD"); err == nil {
		return nil
	}
	_, err := a.git(dir, "-c", "user.name=wmm", "-c", "user.email=wmm@localhost", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "Initialize coordination workspace")
	return err
}

// Keep the supplied config intact: workmux resolves agents, panes, prompts,
// prefixes and all other settings. Only topology is fixed by wmm's workflow.
func (a *app) openAgent(dir, handle, session, role, prompt, configPath, owner string) error {
	before := a.windows(session)
	var placeholder string
	var err error
	if len(before) == 0 {
		placeholder, err = a.command(dir, "tmux", "new-session", "-d", "-P", "-F", "#{window_id}", "-s", session, "-n", "starting", "-c", dir)
		if err != nil {
			return err
		}
		before = a.windows(session)
	}
	args := []string{"workmux", "open", handle, "--mode", "window", "--parent-session", session, "--target-name", role + "-" + digest(owner, 8)}
	if prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	_, openErr := a.command(dir, args...)
	known := map[string]bool{}
	for _, id := range before {
		known[id] = true
	}
	opened := false
	for _, id := range a.windows(session) {
		if known[id] {
			continue
		}
		opened = true
		if _, err = a.command("", "tmux", "set-window-option", "-t", id, "@wmm_workspace", owner); err != nil {
			return err
		}
		if _, err = a.command("", "tmux", "set-window-option", "-t", id, "@wmm_role", role); err != nil {
			return err
		}
	}
	if placeholder != "" && (opened || openErr != nil) {
		if _, err = a.command("", "tmux", "kill-window", "-t", placeholder); err != nil {
			return err
		}
	}
	return openErr
}

func (a *app) openBuilder(c *config, m *manifest, prompt string) error {
	if err := a.bindSession(c, m, false); err != nil {
		return err
	}
	active := a.managedWindows(c, m)
	if active["build"] != "" {
		return a.nameBuilder(active["build"], m.Branch)
	}
	dir := workspace(c, m.Branch)
	if err := a.coordinator(dir); err != nil {
		return err
	}
	// The shared builder has no single source repo. Its project config belongs
	// beside wmm.toml; pass that original file without interpreting or rewriting it.
	configPath := ""
	for _, name := range []string{".workmux.yaml", ".workmux.yml"} {
		candidate := filepath.Join(c.Root, name)
		if exists(candidate) {
			configPath = candidate
			break
		}
	}
	if err := a.openAgent(dir, filepath.Base(dir), m.Session, "build", prompt, configPath, dir); err != nil {
		return err
	}
	build := a.managedWindows(c, m)["build"]
	if err := a.nameBuilder(build, m.Branch); err != nil {
		return err
	}
	previous := active["review"]
	if previous == "" {
		previous = active["debug"]
	}
	if previous != "" {
		if _, err := a.command("", "tmux", "swap-window", "-s", build, "-t", previous); err != nil {
			return err
		}
	}
	m.Phase = "implementation"
	if err := save(c, m); err != nil {
		return err
	}
	// The invoking pane may live in a replaced window, so close them last.
	for name, id := range active {
		if name == "debug" || isReview(name) {
			if _, err := a.command("", "tmux", "kill-window", "-t", id); err != nil {
				return err
			}
		}
	}
	a.focus(m.Session)
	return nil
}
func (a *app) nameBuilder(window, branch string) error {
	if window == "" {
		return fail("Builder window was not created.", "Retry the same wmm start command.")
	}
	_, err := a.command("", "tmux", "rename-window", "-t", window, branch)
	return err
}
func (a *app) focus(session string) {
	if os.Getenv("TMUX") != "" {
		a.run("", "tmux", "switch-client", "-t", session)
	}
}

// Resolve the invoking pane explicitly: the active window can change while
// workmux launches agents, and another tmux client may have a different focus.
func (a *app) currentWindow() (string, error) {
	if os.Getenv("TMUX") == "" {
		return "", nil
	}
	return a.tmuxContext("#{window_id}")
}

func (a *app) tmuxContext(format string) (string, error) {
	args := []string{"tmux", "display-message", "-p"}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		args = append(args, "-t", pane)
	}
	return a.command("", append(args, format)...)
}

func (a *app) reviewPanes(window string) (map[string]string, error) {
	text, err := a.command("", "tmux", "list-panes", "-t", window, "-F", "#{pane_id}\t#{@wmm_repo}")
	if err != nil {
		return nil, err
	}
	panes := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		id, repo, _ := strings.Cut(line, "\t")
		if id != "" {
			panes[id] = repo
		}
	}
	return panes, nil
}

func (a *app) openReviews(c *config, m *manifest, preparePR bool) error {
	for _, r := range m.Repos {
		if !r.Ready {
			return fail("Provisioning is incomplete.", "Rerun wmm start with the original branch and repositories first.")
		}
		if err := a.verify(r, m.Branch); err != nil {
			return err
		}
	}
	current, err := a.currentWindow()
	if err != nil {
		return err
	}
	if err = a.bindSession(c, m, true); err != nil {
		return err
	}
	active := a.managedWindows(c, m)
	target := active["review"]
	if target == "" && current != "" && current == active["build"] {
		target = current
	}
	dir := workspace(c, m.Branch)
	markTarget := func() error {
		if _, err := a.command("", "tmux", "set-window-option", "-t", target, "@wmm_role", "review"); err != nil {
			return err
		}
		m.Phase = "review"
		return save(c, m)
	}
	if target != "" {
		if err = markTarget(); err != nil {
			return err
		}
	}
	for _, r := range m.Repos {
		role := "review-" + r.Alias
		source := a.managedWindows(c, m)[role]
		if target != "" && source == "" {
			panes, err := a.reviewPanes(target)
			if err != nil {
				return err
			}
			found := false
			for _, repo := range panes {
				if repo == r.Alias {
					found = true
				}
			}
			if found {
				continue
			}
		}
		if source == "" {
			prompt := ""
			if preparePR {
				prompt = fmt.Sprintf("Review %s against base commit %s (%s), including staged, unstaged, and untracked changes. Prepare a local PR title and description including cross-repo dependencies and validation. Do not push or publish the PR; wait for explicit user authorization.", r.Alias, r.BaseCommit, r.Base)
			}
			if err = a.openAgent(r.Source, r.Handle, m.Session, role, prompt, "", dir); err != nil {
				return err
			}
			source = a.managedWindows(c, m)[role]
			if source == "" {
				return fail("Review window was not created for "+r.Alias, "Inspect workmux output and retry wmm review.")
			}
		}
		panes, err := a.reviewPanes(source)
		if err != nil {
			return err
		}
		for _, pane := range keys(panes) {
			if _, err = a.command("", "tmux", "set-option", "-p", "-t", pane, "@wmm_repo", r.Alias); err != nil {
				return err
			}
		}
		if target == "" {
			target = source
			if err = markTarget(); err != nil {
				return err
			}
		} else if source != target {
			// Let workmux launch its configured panes, then move the live processes.
			// Tag before moving so an interrupted transfer can be retried safely.
			for _, pane := range keys(panes) {
				if _, err = a.command("", "tmux", "join-pane", "-d", "-s", pane, "-t", target, "-l", "1"); err != nil {
					return err
				}
				if _, err = a.command("", "tmux", "select-layout", "-t", target, "tiled"); err != nil {
					return err
				}
			}
		}
	}
	if target != "" {
		if _, err = a.command("", "tmux", "select-window", "-t", target); err != nil {
			return err
		}
	}
	a.focus(m.Session)
	return nil
}
