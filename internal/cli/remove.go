package cli

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// detachedRemovalEnv marks the copy of wmm remove relaunched on the tmux server, so it
// finishes the cleanup instead of handing it off again.
const detachedRemovalEnv = "WMM_DETACHED_REMOVE"

func (a *app) remove(c *config, o options) error {
	dir := workspace(c, o.branch)
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		a.scalar("branch", o.branch)
		a.scalar("state", "already removed")
		return nil
	}
	m, err := load(c, o.branch)
	if err != nil {
		return err
	}
	if err := a.checkRemoval(c, dir, m, o); err != nil {
		return err
	}
	if o.dryRun {
		a.scalar("branch", m.Branch)
		rows := [][]any{}
		for _, r := range m.Repos {
			if !r.Removed {
				rows = append(rows, []any{r.Alias, r.Path})
			}
		}
		a.table("repos", []string{"repo", "path"}, rows)
		a.scalar("keep_branch", o.keepBranch)
		return nil
	}
	// workmux remove closes the change window, which would end a wmm running inside it
	// before it deletes branches and the workspace.
	if os.Getenv(detachedRemovalEnv) == "" && a.insideChangeWindow(m.Session, dir) {
		return a.removeInBackground(c, m.Branch, o)
	}
	if err := a.removeChecked(c, dir, m, o); err != nil {
		return err
	}
	a.scalar("branch", m.Branch)
	a.scalar("state", "removed")
	a.scalar("kept_branches", o.keepBranch)
	return nil
}

// checkRemoval validates the whole group before closing windows or removing any repo.
func (a *app) checkRemoval(c *config, dir string, m *manifest, o options) error {
	if err := a.checkRemovalWorkspace(dir, m, o.force); err != nil {
		return err
	}
	for i := range m.Repos {
		if err := a.checkRemovalRepo(c, m, &m.Repos[i], o); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) removeChecked(c *config, dir string, m *manifest, o options) error {
	if len(m.Repos) > 0 {
		if err := a.prerequisites(c); err != nil {
			return err
		}
	}
	// Removal may close the terminal running wmm. Finish saving/cleaning state
	// even if its shell sends SIGHUP or the output pipe disappears.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGPIPE)
	defer signal.Stop(signals)
	m.Phase = phaseRemoving
	if err := save(c, m); err != nil {
		return err
	}
	// Strict workspace ownership prevents closing unrelated windows, including
	// user-created windows in a dedicated wmm session. Closing them first keeps workmux
	// from mistaking the focused change window for its own and deferring the removal.
	for _, id := range a.windows(m.Session, dir) {
		if _, err := a.command(c.Root, "tmux", "kill-window", "-t", id); err != nil {
			return err
		}
	}
	for i := range m.Repos {
		r := &m.Repos[i]
		if r.Removed {
			continue
		}
		// Recheck immediately before mutation as other processes can edit repos.
		if err := a.checkRemovalRepo(c, m, r, o); err != nil {
			return err
		}
		actual, err := a.worktreeFor(*r, m.Branch)
		if err != nil {
			return err
		}
		if actual != "" {
			args := []string{"workmux", "remove", filepath.Base(actual), "--keep-branch"}
			if o.force {
				args = append(args, "--force")
			}
			if _, err := a.command(r.Source, args...); err != nil {
				return err
			}
			remaining, err := a.worktreeFor(*r, m.Branch)
			if err != nil {
				return err
			}
			if remaining != "" {
				return fail("Worktree removal is still pending for "+r.Alias, "Repeat the same wmm remove command to finish cleanup.")
			}
		} else if r.Path != "" && exists(r.Path) {
			// checkRemovalRepo only lets an unregistered worktree through with --force.
			if err := os.RemoveAll(r.Path); err != nil {
				return err
			}
		}
		if !o.keepBranch {
			if _, err := a.tryGit(r.Source, "show-ref", "--verify", "--quiet", "refs/heads/"+m.Branch); err == nil {
				flag := "-d"
				if o.force {
					flag = "-D"
				}
				if _, err := a.git(r.Source, "branch", flag, "--", m.Branch); err != nil {
					return fail("Local branch preserved in "+r.Alias, "Retry wmm remove with --keep-branch to retain commits, or --force to discard them.")
				}
			}
		}
		r.Removed = true
		if err := save(c, m); err != nil {
			return err
		}
	}
	if err := a.checkRemovalWorkspace(dir, m, o.force); err != nil {
		return err
	}
	// RemoveAll unlinks repository symlinks rather than following them.
	return os.RemoveAll(dir)
}

func (a *app) checkRemovalRepo(c *config, m *manifest, r *repoState, o options) error {
	if r.Removed {
		return nil
	}
	if c.Repos[r.Alias] != r.Source {
		return fail("Repository path changed for "+r.Alias, "Restore the original mapping in wmm.toml before removing the change.")
	}
	actual, err := a.worktreeFor(*r, m.Branch)
	if err != nil {
		return err
	}
	if actual != "" {
		if r.Path != "" {
			left, e1 := canonical(actual)
			right, e2 := canonical(r.Path)
			if e1 != nil || e2 != nil || left != right {
				return fail("Worktree path changed for "+r.Alias, "Restore the recorded worktree before removing this change.")
			}
		} else if filepath.Base(actual) != r.Handle {
			return fail("Unrecognized worktree for "+r.Alias, "Inspect wmm status for this branch; no unrelated worktree will be removed.")
		}
		r.Path = actual
		if !o.force {
			dirty, err := a.git(actual, "status", "--porcelain", "--untracked-files=all", "--ignored", "--ignore-submodules=none")
			if err != nil {
				return err
			}
			if dirty != "" {
				return fail("Files would be lost in "+r.Alias, "Save the files first, or repeat wmm remove with --force to discard them.")
			}
		}
	} else if r.Path != "" && exists(r.Path) {
		registered, err := a.registered(*r)
		if err != nil {
			return err
		}
		if registered {
			return fail("Recorded worktree changed in "+r.Alias, "Restore its original branch before removing this change.")
		}
		if filepath.Base(r.Path) != r.Handle {
			return fail("Unrecognized worktree for "+r.Alias, "Inspect wmm status for this branch; no unrelated worktree will be removed.")
		}
		if !o.force {
			return fail("Files would be lost in "+r.Alias+": its worktree is no longer registered in Git", "Save the files first, or repeat with --force to discard them.")
		}
	}
	if !o.keepBranch && !o.force {
		if _, err := a.tryGit(r.Source, "show-ref", "--verify", "--quiet", "refs/heads/"+m.Branch); err == nil {
			if _, err := a.tryGit(r.Source, "merge-base", "--is-ancestor", "refs/heads/"+m.Branch, r.Base); err != nil {
				return fail("Unmerged commits in "+r.Alias, "Use wmm remove with --keep-branch to retain commits, or --force to discard them.")
			}
		}
	}
	return nil
}

func (a *app) checkRemovalWorkspace(dir string, m *manifest, force bool) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail("Workspace directory was replaced.", "Restore the original workspace directory before removing it.")
	}
	if force {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// BRIEF.md and the prompt files were generated by earlier wmm versions.
	allowed := map[string]bool{"manifest.json": true, ".git": true, ".workmux": true, "BRIEF.md": true, "build-prompt.md": true}
	for _, r := range m.Repos {
		allowed["review-"+r.Alias+"-prompt.md"] = true
		if info, err := os.Lstat(filepath.Join(dir, r.Alias)); err == nil && info.Mode()&os.ModeSymlink != 0 {
			allowed[r.Alias] = true
		}
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fail("Workspace contains extra files: "+entry.Name(), "Save these files elsewhere, or use wmm remove --force to discard them.")
		}
	}
	if exists(filepath.Join(dir, ".git")) {
		files, err := a.git(dir, "ls-files")
		if err != nil {
			return err
		}
		if files != "" {
			return fail("The coordination repository contains tracked files.", "Save this work elsewhere, or use wmm remove --force to discard it.")
		}
	}
	return nil
}

func (a *app) insideChangeWindow(session, dir string) bool {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return false
	}
	current, err := a.run("", "tmux", "display-message", "-p", "-t", pane, "#{window_id}")
	if err != nil {
		return false
	}
	for _, id := range a.windows(session, dir) {
		if id == strings.TrimSpace(current) {
			return true
		}
	}
	return false
}

func (a *app) removeInBackground(c *config, branch string, o options) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	log := filepath.Join(c.State, "remove.log")
	args := []string{detachedRemovalEnv + "=1", shellQuote(self), "remove", shellQuote(branch), "--config", shellQuote(c.Path)}
	if o.force {
		args = append(args, "--force")
	}
	if o.keepBranch {
		args = append(args, "--keep-branch")
	}
	script := strings.Join(args, " ") + " >> " + shellQuote(log) + " 2>&1 || tmux display-message " + shellQuote("wmm remove "+branch+" failed; see "+log)
	if _, err := a.command(c.Root, "tmux", "run-shell", "-b", "-c", c.Root, script); err != nil {
		return err
	}
	a.scalar("branch", branch)
	a.scalar("state", "removing in background")
	a.scalar("log", log)
	return nil
}

// lockPatiently waits for the lock that the wmm which relaunched this removal still holds
// while it exits.
func lockPatiently(c *config, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		unlock, err := lock(c)
		if err == nil || time.Now().After(deadline) {
			return unlock, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
