package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type repoState struct {
	Alias      string `json:"alias"`
	Source     string `json:"source"`
	Base       string `json:"base"`
	BaseCommit string `json:"base_commit"`
	Path       string `json:"path"`
	Handle     string `json:"handle"`
	Ready      bool   `json:"ready"`
}
type manifest struct {
	Version   int         `json:"version"`
	Branch    string      `json:"branch"`
	Phase     string      `json:"phase"`
	Objective string      `json:"objective"`
	Session   string      `json:"session"`
	Repos     []repoState `json:"repos"`
}

func digest(s string, n int) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:n]
}
func changeID(branch string) string {
	slug := regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(branch, "-")
	if len(slug) > 48 {
		slug = slug[:48]
	}
	return slug + "-" + digest(branch, 10)
}
func workspace(c *config, branch string) string { return filepath.Join(c.State, changeID(branch)) }
func save(c *config, m *manifest) error {
	return writeJSON(filepath.Join(workspace(c, m.Branch), "manifest.json"), m)
}
func load(c *config, branch string) (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(workspace(c, branch), "manifest.json"))
	if err != nil {
		return nil, fail("Change not found: "+branch, "Run wmm status to list changes, or wmm start <branch> <repos...>.")
	}
	var m manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Version != 1 || m.Branch != branch || len(m.Repos) == 0 {
		return nil, fail("Unsupported or mismatched workspace manifest.", "Restore the manifest from a backup; existing worktrees were preserved.")
	}
	return &m, nil
}
func lock(c *config) (func(), error) {
	if err := os.MkdirAll(c.State, 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(c.State, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fail("Another wmm mutation is running in this workspace.", "Retry after that command finishes.")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func (a *app) plan(c *config, branch string, aliases []string) ([]repoState, error) {
	if len(aliases) == 0 {
		return nil, usage("Choose at least one repository.", help("start"))
	}
	seen := map[string]bool{}
	for _, alias := range aliases {
		if c.Repos[alias] == "" {
			return nil, usage("Unknown repository: "+alias, "Configured aliases: "+strings.Join(keys(c.Repos), ", "))
		}
		if seen[alias] {
			return nil, usage("Duplicate repository: "+alias, help("start"))
		}
		seen[alias] = true
	}
	if strings.HasPrefix(branch, "-") {
		return nil, usage("Invalid branch: "+branch, help("start"))
	}
	if _, err := a.tryGit(c.Root, "check-ref-format", "refs/heads/"+branch); err != nil {
		return nil, usage("Invalid branch: "+branch, help("start"))
	}
	var records []repoState
	commons := map[string]bool{}
	for _, alias := range aliases {
		source := c.Repos[alias]
		root, err := a.tryGit(source, "rev-parse", "--show-toplevel")
		if err != nil {
			return nil, fail("Not a repository root: "+source, "Correct the path in wmm.toml.")
		}
		real, err := canonical(root)
		if err != nil || real != source {
			return nil, fail("Not a repository root: "+source, "Correct the path in wmm.toml.")
		}
		common, err := a.git(source, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return nil, err
		}
		if commons[common] {
			return nil, usage("Selected paths share the same Git repository.", "Select each repository once.")
		}
		commons[common] = true
		if _, err = a.tryGit(source, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			return nil, fail("Branch already exists in "+alias+": "+branch, "Choose a new branch; wmm never adopts unrelated existing branches.")
		}
		base, _ := a.tryGit(source, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		if base == "" {
			base = "origin/<remote-default>"
		}
		records = append(records, repoState{Alias: alias, Source: source, Base: base, Handle: "wmm-" + changeID(branch) + "-" + alias})
	}
	return records, nil
}
func (a *app) fetchBases(records []repoState) error {
	pattern := regexp.MustCompile(`(?m)^ref: refs/heads/(.+)\s+HEAD$`)
	for i := range records {
		r := &records[i]
		text, err := a.git(r.Source, "ls-remote", "--symref", "origin", "HEAD")
		if err != nil {
			return err
		}
		match := pattern.FindStringSubmatch(text)
		if len(match) != 2 {
			return fail("Remote default branch unavailable for "+r.Alias, "Configure origin with a default branch, then retry wmm start.")
		}
		branch := match[1]
		if _, err = a.git(r.Source, "fetch", "origin", "+refs/heads/"+branch+":refs/remotes/origin/"+branch); err != nil {
			return err
		}
		if _, err = a.git(r.Source, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch); err != nil {
			return err
		}
		r.Base = "origin/" + branch
		r.BaseCommit, err = a.git(r.Source, "rev-parse", "refs/remotes/origin/"+branch+"^{commit}")
		if err != nil {
			return err
		}
	}
	return nil
}
func (a *app) worktreeFor(r repoState, branch string) (string, error) {
	text, err := a.git(r.Source, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(text, "\x00\x00") {
		var path, ref string
		for _, line := range strings.Split(entry, "\x00") {
			key, value, ok := strings.Cut(line, " ")
			if ok {
				if key == "worktree" {
					path = value
				}
				if key == "branch" {
					ref = value
				}
			}
		}
		if ref == "refs/heads/"+branch {
			return path, nil
		}
	}
	return "", nil
}
func (a *app) verify(r repoState, branch string) error {
	actual, err := a.worktreeFor(r, branch)
	if err != nil {
		return err
	}
	if actual != "" && r.Path != "" {
		left, e1 := canonical(actual)
		right, e2 := canonical(r.Path)
		if e1 == nil && e2 == nil && left == right {
			return nil
		}
	}
	return fail("Worktree missing or branch changed for "+r.Alias, "Restore the recorded worktree and branch before retrying; see wmm status <branch>.")
}
func (a *app) provision(c *config, m *manifest) error {
	for i := range m.Repos {
		r := &m.Repos[i]
		if r.Ready {
			if err := a.verify(*r, m.Branch); err != nil {
				return err
			}
		} else {
			actual, err := a.worktreeFor(*r, m.Branch)
			if err != nil {
				return err
			}
			if actual != "" {
				return fail("Incomplete provisioning in "+r.Alias+": "+actual, "Inspect the failed setup. Preserve any work before removing the incomplete worktree, then retry wmm start.")
			}
			text, err := a.command(r.Source, "workmux", "add", m.Branch, "--headless", "--json", "--name", r.Handle, "--base", r.BaseCommit)
			if err != nil {
				return err
			}
			var receipt struct {
				Version int    `json:"schema_version"`
				Branch  string `json:"branch"`
				Path    string `json:"worktree_path"`
			}
			if err = json.Unmarshal([]byte(text), &receipt); err != nil || receipt.Version != 1 || receipt.Branch != m.Branch || receipt.Path == "" {
				return fail("Unsupported provisioning receipt.", "Use a workmux release with headless JSON schema version 1.")
			}
			r.Path, err = canonical(receipt.Path)
			if err != nil {
				return err
			}
			if err = a.verify(*r, m.Branch); err != nil {
				return err
			}
			r.Ready = true
			if err = save(c, m); err != nil {
				return err
			}
		}
		link := filepath.Join(workspace(c, m.Branch), r.Alias)
		info, err := os.Lstat(link)
		if err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return fail("Workspace path already exists: "+link, "Move the conflicting file before retrying.")
			}
			real, e := canonical(link)
			if e != nil || real != r.Path {
				return fail("Workspace link points elsewhere: "+link, "Restore the workspace link before retrying.")
			}
		} else if os.IsNotExist(err) {
			if err = os.Symlink(r.Path, link); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	if m.Phase == "provisioning" {
		m.Phase = "ready"
	}
	return save(c, m)
}
