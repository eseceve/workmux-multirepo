package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func sessionName(c *config, branch string) string { return "wmm-" + digest(c.Root+":"+branch, 12) }

// Roles are tmux metadata, independent of workmux's prefix or user renames.
func (a *app) windows(session string) map[string]string {
	text, err := a.run("", "tmux", "list-windows", "-t", "="+session, "-F", "#{window_id}\t#{window_name}\t#{@wmm_role}")
	result := map[string]string{}
	if err != nil {
		return result
	}
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
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
		result[name] = fields[0]
	}
	return result
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
func hasReviews(windows map[string]string) bool {
	for name := range windows {
		if strings.HasPrefix(name, "review-") {
			return true
		}
	}
	return false
}

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
func (a *app) openAgent(dir, handle, session, role, prompt, configPath string) error {
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
	args := []string{"workmux", "open", handle, "--mode", "window", "--parent-session", session, "--target-name", role, "--prompt-file", prompt}
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
		if _, err = a.command("", "tmux", "set-window-option", "-t", id, "@wmm_role", role); err != nil {
			return err
		}
	}
	if opened && placeholder != "" {
		if _, err = a.command("", "tmux", "kill-window", "-t", placeholder); err != nil {
			return err
		}
	}
	return openErr
}

func (a *app) openBuilder(c *config, m *manifest) error {
	active := a.windows(m.Session)
	if hasReviews(active) {
		return fail("Review windows are still open.", "Close the review windows before reopening the shared builder.")
	}
	if active["build"] != "" {
		return nil
	}
	dir := workspace(c, m.Branch)
	context, err := brief(c, m)
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf("Implement the coordinated change described in %s. Read the root workspace instructions and each repository's AGENTS.md / CLAUDE.md before editing. Original workspace: %s. Only edit the worktrees listed in manifest.json, never their source checkouts. Ask for the objective if unspecified. Coordinate contracts across repos and run relevant checks. Update BRIEF.md with integration contracts, verification and a review handoff. Do not push, publish PRs, or merge unless the user explicitly requests it. The Git repository at this workspace root is internal coordination state; make code commits only in the individual repositories. Close this builder window when implementation is ready so wmm review can launch independent reviewers.", context, c.Root)
	path := filepath.Join(dir, "build-prompt.md")
	if err = os.WriteFile(path, []byte(prompt+"\n"), 0644); err != nil {
		return err
	}
	if err = a.coordinator(dir); err != nil {
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
	if err = a.openAgent(dir, filepath.Base(dir), m.Session, "build", path, configPath); err != nil {
		return err
	}
	m.Phase = "implementation"
	if err = save(c, m); err != nil {
		return err
	}
	a.focus(m.Session)
	return nil
}
func (a *app) focus(session string) {
	if os.Getenv("TMUX") != "" {
		a.run("", "tmux", "switch-client", "-t", session)
	}
}
func (a *app) openReviews(c *config, m *manifest, preparePR bool) error {
	if a.windows(m.Session)["build"] != "" {
		return fail("The shared builder window is still open.", "Close the builder window, then rerun wmm review. No agents were stopped.")
	}
	for _, r := range m.Repos {
		if !r.Ready {
			return fail("Provisioning is incomplete.", "Rerun wmm start with the original branch and repositories first.")
		}
		if err := a.verify(r, m.Branch); err != nil {
			return err
		}
	}
	dir := workspace(c, m.Branch)
	context, err := brief(c, m)
	if err != nil {
		return err
	}
	for _, r := range m.Repos {
		target := "review-" + r.Alias
		if a.windows(m.Session)[target] != "" {
			continue
		}
		prompt := fmt.Sprintf("Review only %s in %s. Read %s and the repository's AGENTS.md / CLAUDE.md. Compare against original base commit %s (%s); include staged, unstaged, and untracked changes, not just commits. Inspect sibling repositories for contract compatibility but do not edit them. Find correctness defects, fix this repository's issues, and run its relevant checks. Write findings and validation to %s. Do not merge. ", r.Alias, r.Path, context, r.BaseCommit, r.Base, filepath.Join(dir, "review-"+r.Alias+".md"))
		if preparePR {
			prompt += "Prepare a local PR title and description including cross-repo dependencies and validation. Do not push or publish the PR; wait for explicit user authorization."
		} else {
			prompt += "Do not push or publish PRs."
		}
		path := filepath.Join(dir, "review-"+r.Alias+"-prompt.md")
		if err = os.WriteFile(path, []byte(prompt+"\n"), 0644); err != nil {
			return err
		}
		if err = a.openAgent(r.Source, r.Handle, m.Session, target, path, ""); err != nil {
			return err
		}
		m.Phase = "review"
		if err = save(c, m); err != nil {
			return err
		}
	}
	a.focus(m.Session)
	return nil
}
