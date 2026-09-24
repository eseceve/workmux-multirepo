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

type config struct {
	Repos             map[string]string `toml:"repos"`
	Path, Root, State string            `toml:"-"`
}

type userConfig struct {
	ReviewWorkmuxConfig string `toml:"review_workmux_config"`
}

func (a *app) reviewConfig(o options) (string, error) {
	if o.workmuxConfig != "" {
		if filepath.IsAbs(o.workmuxConfig) {
			return o.workmuxConfig, nil
		}
		return filepath.Join(a.cwd, o.workmuxConfig), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	path := filepath.Join(dir, "wmm", "config.toml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var u userConfig
	if err = toml.NewDecoder(strings.NewReader(string(data))).DisallowUnknownFields().Decode(&u); err != nil {
		return "", usage("Invalid "+path+": "+err.Error(), "Only review_workmux_config is supported.")
	}
	if rest, ok := strings.CutPrefix(u.ReviewWorkmuxConfig, "~/"); ok {
		return filepath.Join(home, rest), nil
	}
	return u.ReviewWorkmuxConfig, nil
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
	var legacy map[string]any
	if err = toml.Unmarshal(data, &legacy); err == nil {
		for _, key := range []string{"build_command", "review_agent"} {
			if _, ok := legacy[key]; ok {
				return nil, usage(key+" is no longer supported in wmm.toml.", "Remove it and configure agent/panes in workmux's global config or .workmux.yaml. Wmm delegates agent launch to workmux.")
			}
		}
	}
	c := &config{}
	if err = toml.NewDecoder(strings.NewReader(string(data))).DisallowUnknownFields().Decode(c); err != nil {
		return nil, usage("Invalid wmm.toml: "+err.Error(), "Use only [repos] with aliases and paths; configure agents and terminal layouts in workmux.")
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
		b.WriteString("# Configure agents and panes in workmux, not here.\n\n[repos]\n")
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
