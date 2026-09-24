package cli

import (
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

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
	if err := a.checkRemovalWorkspace(dir, m, o.force); err != nil {
		return err
	}
	// Validate the whole group before closing windows or removing any repo.
	for i := range m.Repos {
		if err := a.checkRemovalRepo(c, m, &m.Repos[i], o); err != nil {
			return err
		}
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
	// Strict workspace ownership prevents closing unrelated windows, including
	// user-created windows in a dedicated wmm session.
	for _, id := range a.windows(m.Session, dir) {
		if _, err := a.command(c.Root, "tmux", "kill-window", "-t", id); err != nil {
			return err
		}
	}
	if err := a.checkRemovalWorkspace(dir, m, o.force); err != nil {
		return err
	}
	// RemoveAll unlinks repository symlinks rather than following them.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	a.scalar("branch", m.Branch)
	a.scalar("state", "removed")
	a.scalar("kept_branches", o.keepBranch)
	return nil
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
		return fail("Recorded worktree changed in "+r.Alias, "Restore its original branch before removing this change.")
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
	allowed := map[string]bool{"manifest.json": true, ".git": true, ".workmux": true}
	for _, r := range m.Repos {
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
