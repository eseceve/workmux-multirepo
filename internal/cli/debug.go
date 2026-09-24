package cli

import (
	"os"
	"path/filepath"
)

func (a *app) debug(c *config, o options) (err error) {
	if err := a.validateBranch(c, o.branch, "debug"); err != nil {
		return err
	}
	dir := workspace(c, o.branch)
	m := &manifest{Version: 1, Branch: o.branch, Phase: "debug", Session: sessionName(c, o.branch)}
	if exists(filepath.Join(dir, "manifest.json")) {
		existing, err := load(c, o.branch)
		if err != nil {
			return err
		}
		if existing.Phase != "debug" {
			return fail("Change "+o.branch+" already has worktrees (phase: "+existing.Phase+").", "Use wmm start or wmm review for this change.")
		}
		m = existing
	} else {
		if err = os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				os.RemoveAll(dir)
			}
		}()
		if err = save(c, m); err != nil {
			return err
		}
	}
	if err = a.bindSession(c, m, false); err != nil {
		return err
	}
	window := a.managedWindows(c, m)["debug"]
	if window == "" {
		if window, err = a.openShell(c, m, debugIcon+" "+o.branch); err != nil {
			return err
		}
	}
	if err = a.show(window, m.Session); err != nil {
		return err
	}
	a.scalar("branch", o.branch)
	a.scalar("phase", m.Phase)
	a.scalar("directory", c.Root)
	a.scalar("help", "tmux attach -t "+m.Session)
	return nil
}

func (a *app) openShell(c *config, m *manifest, name string) (string, error) {
	args := []string{"tmux", "new-window", "-P", "-F", "#{window_id}", "-t", "=" + m.Session + ":", "-n", name, "-c", c.Root}
	if len(a.windows(m.Session)) == 0 {
		args = []string{"tmux", "new-session", "-d", "-P", "-F", "#{window_id}", "-s", m.Session, "-n", name, "-c", c.Root}
	}
	window, err := a.command(c.Root, args...)
	if err != nil {
		return "", err
	}
	for option, value := range map[string]string{"@wmm_workspace": workspace(c, m.Branch), "@wmm_role": "debug"} {
		if _, err = a.command("", "tmux", "set-window-option", "-t", window, option, value); err != nil {
			return "", err
		}
	}
	return window, nil
}
