package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func sessionName(c *config, branch string) string { return "wmm-" + digest(c.Root+":"+branch, 12) }
func (a *app) windows(session string) map[string]string {
	text, err := a.run("", "tmux", "list-windows", "-t", "="+session, "-F", "#{window_id}\t#{window_name}")
	result := map[string]string{}
	if err != nil {
		return result
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		id, name, ok := strings.Cut(line, "\t")
		if ok {
			result[name] = id
		}
	}
	return result
}
func (a *app) prerequisites(c *config, builder bool) error {
	names := []string{"workmux", "tmux"}
	if builder {
		names = append(names, c.BuildCommand[0])
	}
	for _, name := range names {
		if _, err := a.findCommand(name); err != nil {
			return fail("Executable unavailable: "+name, "Install the README prerequisites or correct build_command in wmm.toml.")
		}
	}
	text, err := a.command(c.Root, "workmux", "add", "--help")
	if err != nil {
		return err
	}
	if !strings.Contains(text, "--headless") || !strings.Contains(text, "--json") {
		return fail("Installed workmux lacks headless provisioning.", "Update workmux to a release supporting add --headless --json.")
	}
	return nil
}
func hasReviews(windows map[string]string) bool {
	for name := range windows {
		if strings.HasPrefix(name, "wmm-review-") {
			return true
		}
	}
	return false
}
func (a *app) openBuilder(c *config, m *manifest) error {
	active := a.windows(m.Session)
	if hasReviews(active) {
		return fail("Review windows are still open.", "Exit the review agents before reopening the shared builder.")
	}
	if active["build"] != "" {
		return nil
	}
	dir := workspace(c, m.Branch)
	context, err := brief(c, m)
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf("Implement the coordinated change described in %s. Read the root workspace instructions and each repository's AGENTS.md / CLAUDE.md before editing. Original workspace: %s. Only edit the worktrees listed in manifest.json, never their source checkouts. Ask for the objective if unspecified. Coordinate contracts across repos and run relevant checks. Update BRIEF.md with integration contracts, verification and a review handoff. Do not push, publish PRs, or merge unless the user explicitly requests it. Exit this agent when implementation is ready so wmm review can launch independent reviewers.", context, c.Root)
	if err = os.WriteFile(filepath.Join(dir, "build-prompt.md"), []byte(prompt+"\n"), 0644); err != nil {
		return err
	}
	var command []string
	for _, arg := range c.BuildCommand {
		switch arg {
		case "{repo_paths}":
			for _, r := range m.Repos {
				command = append(command, r.Path)
			}
		case "{prompt}":
			command = append(command, prompt)
		case "{workspace}":
			command = append(command, dir)
		default:
			command = append(command, arg)
		}
	}
	executable, err := a.findCommand(command[0])
	if err != nil {
		return err
	}
	command[0] = executable
	shellCommand := "exec " + shellJoin(command)
	var id string
	if len(active) > 0 {
		id, err = a.command(dir, "tmux", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", m.Session+":", "-n", "build", "-c", dir, shellCommand)
	} else {
		id, err = a.command(dir, "tmux", "new-session", "-d", "-P", "-F", "#{window_id}", "-s", m.Session, "-n", "build", "-c", dir, shellCommand)
	}
	if err != nil {
		return err
	}
	if id != "" {
		a.run("", "tmux", "set-window-option", "-t", id, "automatic-rename", "off")
		a.run("", "tmux", "rename-window", "-t", id, "build")
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
	active := a.windows(m.Session)
	if active["build"] != "" {
		return fail("The shared builder window is still open.", "Exit the builder agent/window, then rerun wmm review. No agents were stopped.")
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
	configPath := filepath.Join(dir, "review.workmux.yaml")
	// JSON-quoted strings are valid YAML scalars; no YAML parser or shell evaluation is needed.
	data := "agent: " + quote(c.ReviewAgent) + "\nwindow_prefix: wmm-\nmode: window\npanes:\n  - command: <agent>\n    focus: true\n  - split: horizontal\n    size: 12\n"
	if err = os.WriteFile(configPath, []byte(data), 0644); err != nil {
		return err
	}
	var placeholder string
	if len(active) == 0 {
		placeholder, err = a.command(dir, "tmux", "new-session", "-d", "-P", "-F", "#{window_id}", "-s", m.Session, "-n", "starting", "-c", dir)
		if err != nil {
			return err
		}
	}
	defer func() {
		if placeholder != "" && hasReviews(a.windows(m.Session)) {
			a.run("", "tmux", "kill-window", "-t", placeholder)
		}
	}()
	for _, r := range m.Repos {
		target := "review-" + r.Alias
		if a.windows(m.Session)["wmm-"+target] != "" {
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
		if _, err = a.command(r.Source, "workmux", "open", r.Handle, "--mode", "window", "--parent-session", m.Session, "--target-name", target, "--config", configPath, "--prompt-file", path); err != nil {
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
