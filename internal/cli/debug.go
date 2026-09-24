package cli

import "os"

const debugIcon = ""

func (a *app) debug(c *config, o options) error {
	if err := a.validateBranch(c, o.branch, "debug"); err != nil {
		return err
	}
	dir := workspace(c, o.branch)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	m := &manifest{Version: 1, Branch: o.branch, Phase: "debug", Session: sessionName(c, o.branch)}
	if err := save(c, m); err != nil {
		return err
	}
	if err := a.bindSession(c, m, false); err != nil {
		return err
	}
	name := debugIcon + " " + o.branch
	args := []string{"tmux", "new-window", "-P", "-F", "#{window_id}", "-t", "=" + m.Session + ":", "-n", name, "-c", c.Root}
	if len(a.windows(m.Session)) == 0 {
		args = []string{"tmux", "new-session", "-d", "-P", "-F", "#{window_id}", "-s", m.Session, "-n", name, "-c", c.Root}
	}
	window, err := a.command(c.Root, args...)
	if err != nil {
		return err
	}
	for option, value := range map[string]string{"@wmm_workspace": dir, "@wmm_role": "debug"} {
		if _, err = a.command("", "tmux", "set-window-option", "-t", window, option, value); err != nil {
			return err
		}
	}
	a.focus(m.Session)
	a.scalar("branch", o.branch)
	a.scalar("phase", m.Phase)
	a.scalar("directory", c.Root)
	return nil
}
