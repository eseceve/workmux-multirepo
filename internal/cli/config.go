package cli

import (
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var defaultBuild = []string{"claude", "--add-dir", "{repo_paths}", "--", "{prompt}"}

type config struct {
	Repos             map[string]string `toml:"repos"`
	BuildCommand      []string          `toml:"build_command"`
	ReviewAgent       string            `toml:"review_agent"`
	Path, Root, State string            `toml:"-"`
}

func (a *app) discover(explicit string) (*config, error) {
	path := explicit
	if path == "" {
		for dir := a.cwd; ; dir = filepath.Dir(dir) {
			candidate := filepath.Join(dir, "wmm.toml")
			if exists(candidate) {
				path = candidate
				break
			}
			if dir == filepath.Dir(dir) {
				break
			}
		}
	}
	if path == "" {
		return nil, fail("No wmm.toml found.", "Run wmm init in the directory containing your repositories.")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.cwd, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &config{BuildCommand: append([]string{}, defaultBuild...), ReviewAgent: "claude"}
	if err = toml.NewDecoder(strings.NewReader(string(data))).DisallowUnknownFields().Decode(c); err != nil {
		return nil, usage("Invalid wmm.toml: "+err.Error(), "Use repos, build_command, and review_agent; see wmm init --help.")
	}
	c.Path = filepath.Clean(path)
	c.Root = filepath.Dir(c.Path)
	c.State = filepath.Join(c.Root, ".wmm")
	if len(c.Repos) == 0 {
		return nil, usage("No repositories configured.", "Add aliases and paths under [repos] in wmm.toml.")
	}
	for name, path := range c.Repos {
		if !aliasPattern.MatchString(name) || path == "" {
			return nil, usage("Invalid repository alias or path: "+name, "Use api = './my-api' under [repos].")
		}
		if strings.HasPrefix(path, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			path = filepath.Join(home, path[2:])
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.Root, path)
		}
		if real, err := canonical(path); err == nil {
			path = real
		}
		c.Repos[name] = filepath.Clean(path)
	}
	if len(c.BuildCommand) == 0 || c.BuildCommand[0] == "" {
		return nil, usage("build_command must be a nonempty argument array.", "See the README configuration example.")
	}
	for _, arg := range c.BuildCommand {
		if strings.ContainsAny(arg, "{}") && arg != "{workspace}" && arg != "{repo_paths}" && arg != "{prompt}" {
			return nil, usage("Unknown command placeholder: "+arg, "Use whole arguments {workspace}, {repo_paths}, or {prompt}.")
		}
	}
	if strings.TrimSpace(c.ReviewAgent) == "" {
		return nil, usage("review_agent cannot be empty.", "Choose an agent command supported by workmux.")
	}
	return c, nil
}
func (a *app) initialize(o options) error {
	path := o.config
	if path == "" {
		path = "wmm.toml"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.cwd, path)
	}
	if exists(path) {
		a.scalar("config", path)
		a.scalar("state", "already exists")
		return nil
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	repos := map[string]string{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || !exists(filepath.Join(filepath.Dir(path), entry.Name(), ".git")) {
			continue
		}
		name := regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(entry.Name(), "-")
		name = strings.TrimLeft(name, "-_")
		if name == "" || repos[name] != "" {
			return usage("Directory names produce duplicate or empty aliases.", "Create wmm.toml manually with unique aliases.")
		}
		repos[name] = "./" + entry.Name()
	}
	if len(repos) == 0 {
		return fail("No child repositories found.", "Run wmm init from the directory containing your Git repositories.")
	}
	if !o.dryRun {
		var b strings.Builder
		b.WriteString("# Paths are relative to this file. Rename keys to choose shorter aliases.\n")
		b.WriteString("# build_command = [\"claude\", \"--add-dir\", \"{repo_paths}\", \"--\", \"{prompt}\"]\n# review_agent = \"claude\"\n\n[repos]\n")
		for _, name := range keys(repos) {
			fmt.Fprintf(&b, "%s = %s\n", quote(name), quote(repos[name]))
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, err = file.WriteString(b.String())
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	rows := [][]any{}
	for _, name := range keys(repos) {
		rows = append(rows, []any{name, repos[name]})
	}
	a.scalar("config", path)
	a.table("repos", []string{"repo", "path"}, rows)
	a.scalar("help", "Edit aliases in wmm.toml, then run wmm start <branch> <repos...>.")
	return nil
}
