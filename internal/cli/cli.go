package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func Run(args []string, out, diagnostic io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return 1
	}
	a := &app{out: out, err: diagnostic, run: execute, cwd: cwd}
	return a.invoke(args)
}
func (a *app) invoke(args []string) int {
	err := a.dispatch(args)
	if err == nil {
		return 0
	}
	var p *problem
	if !errors.As(err, &p) {
		p = &problem{err.Error(), "Inspect wmm.toml and workspace data; existing worktrees were preserved.", 1}
	}
	a.scalar("error", p.message)
	a.scalar("help", p.hint)
	return p.code
}
func (a *app) dispatch(args []string) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	if o.help {
		fmt.Fprint(a.out, printHelp(o))
		return nil
	}
	if o.command == "init" {
		return a.initialize(o)
	}
	if len(args) == 0 {
		bin, _ := os.Executable()
		a.scalar("bin", bin)
		a.scalar("description", "Coordinate one implementation agent and per-repository reviews.")
	}
	// Validate field names before config lookup or subprocesses.
	if o.command == "status" {
		if err = validateFields(o.fields); err != nil {
			return err
		}
	}
	c, err := a.discover(o.config)
	if err != nil {
		return err
	}
	if o.command == "status" {
		return a.status(c, o)
	}
	if !o.dryRun {
		unlock, err := lock(c)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if o.command == "start" {
		return a.start(c, o)
	}
	m, err := load(c, o.branch)
	if err != nil {
		return err
	}
	if o.dryRun {
		a.scalar("branch", o.branch)
		rows := [][]any{}
		for _, r := range m.Repos {
			rows = append(rows, []any{r.Alias, r.Path})
		}
		a.table("repos", []string{"repo", "path"}, rows)
		a.scalar("prepare_pr", o.preparePR)
		return nil
	}
	if err = a.prerequisites(c); err != nil {
		return err
	}
	if err = a.openReviews(c, m, o.preparePR); err != nil {
		return err
	}
	a.scalar("branch", o.branch)
	a.scalar("phase", m.Phase)
	a.scalar("help", "tmux attach -t "+m.Session)
	return nil
}
func (a *app) start(c *config, o options) error {
	dir := workspace(c, o.branch)
	existing := exists(filepath.Join(dir, "manifest.json"))
	var m *manifest
	var records []repoState
	var err error
	if existing {
		m, err = load(c, o.branch)
		if err != nil {
			return err
		}
		aliases := []string{}
		for _, r := range m.Repos {
			aliases = append(aliases, r.Alias)
			if c.Repos[r.Alias] != r.Source {
				return fail("Repository paths changed since this workspace was created.", "Restore the original paths in wmm.toml or use a new change name.")
			}
		}
		if !slices.Equal(o.repos, aliases) {
			return fail("This change has a different repository selection.", "Use the original aliases in the same order, or choose a new branch.")
		}
		records = m.Repos
	} else {
		records, err = a.plan(c, o.branch, o.repos)
		if err != nil {
			return err
		}
	}
	if o.dryRun {
		a.scalar("branch", o.branch)
		a.scalar("workspace", dir)
		a.scalar("fetch", "on execution (dry-run uses local refs)")
		rows := [][]any{}
		for _, r := range records {
			rows = append(rows, []any{r.Alias, r.Base, r.Handle})
		}
		a.table("repos", []string{"repo", "base", "handle"}, rows)
		return nil
	}
	if err = a.prerequisites(c); err != nil {
		return err
	}
	if !existing {
		if err = a.fetchBases(records); err != nil {
			return err
		}
		if err = os.Mkdir(dir, 0755); err != nil {
			return err
		}
		m = &manifest{Version: 1, Branch: o.branch, Phase: "provisioning", Repos: records, Objective: o.prompt, Session: sessionName(c, o.branch)}
		if err = save(c, m); err != nil {
			return err
		}
	}
	// Also repair workspace links if a previous run stopped after saving a receipt.
	if err = a.provision(c, m); err != nil {
		return err
	}

	if !o.noOpen {
		if err = a.openBuilder(c, m, o.prompt); err != nil {
			return err
		}
	}
	a.scalar("branch", o.branch)
	a.scalar("phase", m.Phase)
	a.scalar("workspace", dir)
	if !o.noOpen {
		a.scalar("help", "tmux attach -t "+m.Session)
	} else {
		a.scalar("help", "wmm review "+o.branch)
	}
	return nil
}
func validateFields(value string) error {
	seen := map[string]bool{}
	for _, field := range strings.Split(value, ",") {
		if seen[field] || !slices.Contains([]string{"repo", "state", "commits", "base", "path", "base_commit"}, field) {
			return usage("Invalid --fields selection.", help("status"))
		}
		seen[field] = true
	}
	return nil
}
func (a *app) status(c *config, o options) error {
	if o.branch == "" {
		paths, err := filepath.Glob(filepath.Join(c.State, "*", "manifest.json"))
		if err != nil {
			return err
		}
		rows := [][]any{}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var m manifest
			if err = json.Unmarshal(data, &m); err != nil {
				return err
			}
			rows = append(rows, []any{m.Branch, m.Phase, len(m.Repos)})
		}
		a.table("changes", []string{"branch", "phase", "repos"}, rows)
		if len(rows) == 0 {
			a.scalar("help", "wmm start <branch> <repos...>")
		} else {
			a.scalar("help", "wmm status <branch>")
		}
		return nil
	}
	m, err := load(c, o.branch)
	if err != nil {
		return err
	}
	fields := strings.Split(o.fields, ",")
	rows := [][]any{}
	for _, r := range m.Repos {
		state := "incomplete"
		commits := 0
		if r.Ready && r.Path != "" {
			if a.verify(r, m.Branch) != nil {
				state = "missing-or-switched"
			} else {
				dirty, err := a.git(r.Path, "status", "--porcelain")
				if err != nil {
					return err
				}
				state = "clean"
				if dirty != "" {
					state = "modified"
				}
				count, err := a.git(r.Path, "rev-list", "--count", r.BaseCommit+"..HEAD")
				if err != nil {
					return err
				}
				commits, err = strconv.Atoi(count)
				if err != nil {
					return err
				}
			}
		}
		values := map[string]any{"repo": r.Alias, "state": state, "commits": commits, "base": r.Base, "path": r.Path, "base_commit": r.BaseCommit}
		row := []any{}
		for _, field := range fields {
			row = append(row, values[field])
		}
		rows = append(rows, row)
	}
	a.scalar("branch", o.branch)
	a.scalar("phase", m.Phase)
	a.scalar("session", m.Session)
	a.table("repos", fields, rows)
	return nil
}
