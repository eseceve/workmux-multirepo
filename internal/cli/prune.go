package cli

func (a *app) prune(c *config, o options) error {
	all, err := changes(c)
	if err != nil {
		return err
	}
	rows := [][]any{}
	kept := 0
	for i := range all {
		m := &all[i]
		abandoned, err := a.abandoned(m)
		if err == nil && !abandoned {
			continue
		}
		dir := workspace(c, m.Branch)
		result := "removed"
		if o.dryRun {
			result = "would remove"
		}
		if err == nil {
			err = a.checkRemoval(c, dir, m, o)
		}
		if err == nil && a.insideChangeWindow(m.Session, dir) {
			err = fail("This is the current window.", "Run wmm prune from another window.")
		}
		if err == nil && !o.dryRun {
			err = a.removeChecked(c, dir, m, o)
		}
		if err != nil {
			result = "kept: " + err.Error()
			kept++
		}
		rows = append(rows, []any{m.Branch, m.Phase, result})
	}
	if len(rows) == 0 {
		a.scalar("state", "nothing to prune")
		return nil
	}
	a.table("changes", []string{"branch", "phase", "result"}, rows)
	if kept > 0 && !o.dryRun {
		return fail("Some abandoned changes were kept.", "Save what each one would lose, then repeat wmm prune with --force to discard it.")
	}
	return nil
}

func (a *app) abandoned(m *manifest) (bool, error) {
	if m.Phase == phaseRemoving {
		return true, nil
	}
	if m.Phase == phaseDebug {
		return false, nil
	}
	for _, r := range m.Repos {
		if r.Removed {
			continue
		}
		path, err := a.worktreeFor(r, m.Branch)
		if err != nil || path != "" {
			return false, err
		}
	}
	return true, nil
}
