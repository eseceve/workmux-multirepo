package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fixture struct {
	t               *testing.T
	root            string
	a               *app
	out, diagnostic bytes.Buffer
	repos           map[string]string
	calls           [][]string
	windows         map[string]string
	owners          map[string]string
	panes           map[string]string
	paneRepos       map[string]string
	nextWindow      int
	order           []string
	panesPerWindow  int
	failAlias       string
	failAfterCreate bool
	failRemoveAlias string
}

func gitTest(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, repos: map[string]string{}, windows: map[string]string{}, owners: map[string]string{}, panes: map[string]string{}, paneRepos: map[string]string{}}
	for _, item := range []struct{ name, base string }{{"api", "release"}, {"web", "develop"}} {
		repo := filepath.Join(root, item.name+" repo")
		remote := filepath.Join(root, item.name+".git")
		gitTest(t, root, "init", "--bare", "-b", item.base, remote)
		gitTest(t, root, "init", "-b", item.base, repo)
		gitTest(t, repo, "config", "user.name", "Test")
		gitTest(t, repo, "config", "user.email", "test@example.com")
		gitTest(t, repo, "config", "commit.gpgsign", "false")
		if err = os.WriteFile(filepath.Join(repo, "file.txt"), []byte("initial\n"), 0644); err != nil {
			t.Fatal(err)
		}
		gitTest(t, repo, "add", ".")
		gitTest(t, repo, "commit", "-m", "init")
		gitTest(t, repo, "remote", "add", "origin", remote)
		gitTest(t, repo, "push", "origin", item.base)
		gitTest(t, repo, "remote", "set-head", "origin", "-a")
		f.repos[item.name] = repo
	}
	if err = os.WriteFile(filepath.Join(root, "wmm.toml"), []byte("[repos]\napi = './api repo'\nweb = './web repo'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f.a = &app{cwd: root, out: &f.out, err: &f.diagnostic, run: f.run, lookup: func(s string) (string, error) { return "/bin/" + s, nil }}
	return f
}
func flagValue(args []string, key string) string {
	for i, arg := range args {
		if arg == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
func (f *fixture) run(cwd string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{}, args...))
	if slices.Equal(args, []string{"workmux", "add", "--help"}) {
		return "--headless --json", nil
	}
	if len(args) >= 2 && args[0] == "workmux" && args[1] == "add" {
		alias := "api"
		if cwd == f.repos["web"] {
			alias = "web"
		}
		if alias == f.failAlias && !f.failAfterCreate {
			return "", errors.New("simulated setup failure")
		}
		path := filepath.Join(f.root, "worktrees", alias, flagValue(args, "--name"))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return "", err
		}
		if _, err := execute(cwd, "git", "show-ref", "--verify", "refs/heads/"+args[2]); err == nil {
			gitTest(f.t, cwd, "worktree", "add", path, args[2])
		} else {
			gitTest(f.t, cwd, "worktree", "add", "-b", args[2], path, flagValue(args, "--base"))
		}
		if alias == f.failAlias {
			return "", errors.New("simulated hook failure after creation")
		}
		data, _ := json.Marshal(map[string]any{"schema_version": 1, "branch": args[2], "worktree_path": path})
		return string(data), nil
	}
	if len(args) >= 2 && args[0] == "workmux" && args[1] == "remove" {
		if !slices.Contains(args, "--keep-branch") {
			f.t.Fatal("worktree removal must preserve the branch for separate handling")
		}
		alias := "api"
		if cwd == f.repos["web"] {
			alias = "web"
		}
		if alias == f.failRemoveAlias {
			return "", errors.New("simulated removal failure")
		}
		gitArgs := []string{"git", "worktree", "remove"}
		if slices.Contains(args, "--force") {
			gitArgs = append(gitArgs, "--force")
		}
		return execute(cwd, append(gitArgs, filepath.Join(f.root, "worktrees", alias, args[2]))...)
	}
	if len(args) >= 2 && args[0] == "workmux" && args[1] == "open" {
		f.nextWindow++
		f.windows["wmm-"+flagValue(args, "--target-name")] = fmt.Sprintf("@%d", f.nextWindow)
		f.order = append(f.order, fmt.Sprintf("@%d", f.nextWindow))
		for i := 0; i < max(f.panesPerWindow, 1); i++ {
			f.panes[fmt.Sprintf("%%%d.%d", f.nextWindow, i)] = fmt.Sprintf("@%d", f.nextWindow)
		}
		return "", nil
	}
	if len(args) >= 2 && args[0] == "tmux" {
		switch args[1] {
		case "display-message":
			return "current", nil
		case "list-panes":
			var out strings.Builder
			for _, pane := range keys(f.panes) {
				if f.panes[pane] == flagValue(args, "-t") {
					fmt.Fprintf(&out, "%s\t%s\n", pane, f.paneRepos[pane])
				}
			}
			return out.String(), nil
		case "set-option":
			f.paneRepos[flagValue(args, "-t")] = args[len(args)-1]
		case "join-pane":
			pane := flagValue(args, "-s")
			old := f.panes[pane]
			f.panes[pane] = flagValue(args, "-t")
			remaining := false
			for _, window := range f.panes {
				if window == old {
					remaining = true
				}
			}
			if !remaining {
				f.closeWindow(old)
			}

		case "list-windows":
			if len(f.windows) == 0 {
				return "", errors.New("no session")
			}
			var out strings.Builder
			for _, name := range keys(f.windows) {
				fmt.Fprintf(&out, "%s\t%s\t%s\t%s\n", f.windows[name], name, name, f.owners[f.windows[name]])
			}
			return out.String(), nil
		case "new-session", "new-window":
			f.nextWindow++
			id := fmt.Sprintf("@%d", f.nextWindow)
			f.windows[flagValue(args, "-n")] = id
			f.order = append(f.order, id)
			return id, nil
		case "kill-window":
			f.closeWindow(flagValue(args, "-t"))
		case "swap-window":
			source, target := slices.Index(f.order, flagValue(args, "-s")), slices.Index(f.order, flagValue(args, "-t"))
			if source < 0 || target < 0 {
				return "", errors.New("can't find window")
			}
			f.order[source], f.order[target] = f.order[target], f.order[source]
		case "set-window-option":
			if len(args) > 5 && args[4] == "@wmm_workspace" {
				f.owners[args[3]] = args[5]
			}
			if len(args) > 5 && args[4] == "@wmm_role" {
				for name, id := range f.windows {
					if id == args[3] {
						delete(f.windows, name)
						f.windows[args[5]] = id
						break
					}
				}
			}
		}
		return "", nil
	}
	return execute(cwd, args...)
}
func (f *fixture) closeWindow(window string) {
	for name, id := range f.windows {
		if id == window {
			delete(f.windows, name)
		}
	}
	f.order = slices.DeleteFunc(f.order, func(id string) bool { return id == window })
}
func (f *fixture) cli(args ...string) (int, string) {
	f.out.Reset()
	f.diagnostic.Reset()
	code := f.a.invoke(args)
	return code, f.out.String()
}
func (f *fixture) mustCLI(args ...string) string {
	f.t.Helper()
	code, out := f.cli(args...)
	if code != 0 {
		f.t.Fatalf("wmm %v: %d\n%s\n%s", args, code, out, f.diagnostic.String())
	}
	return out
}
func (f *fixture) config() *config {
	f.t.Helper()
	c, err := f.a.discover("")
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}
func (f *fixture) manifest() *manifest {
	f.t.Helper()
	m, err := load(f.config(), "feat/shared")
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}
func (f *fixture) start() *manifest {
	f.t.Helper()
	f.mustCLI("start", "feat/shared", "api", "web", "--no-open")
	return f.manifest()
}
func (f *fixture) count(command, sub string) int {
	count := 0
	for _, args := range f.calls {
		if len(args) > 1 && args[0] == command && args[1] == sub && !slices.Contains(args, "--help") {
			count++
		}
	}
	return count
}

func TestRemoteBasesPinnedAndSourcesUntouched(t *testing.T) {
	f := newFixture(t)
	gitTest(t, f.repos["api"], "switch", "-c", "other-local-feature")
	m := f.start()
	if m.Repos[0].Base != "origin/release" || m.Repos[1].Base != "origin/develop" {
		t.Fatal(m.Repos)
	}
	for _, r := range m.Repos {
		if gitTest(t, r.Path, "rev-parse", "HEAD") != r.BaseCommit {
			t.Fatal("base mismatch")
		}
		if gitTest(t, r.Path, "branch", "--show-current") != "feat/shared" {
			t.Fatal("branch mismatch")
		}
		if _, err := os.Readlink(filepath.Join(workspace(f.config(), m.Branch), r.Alias)); err != nil {
			t.Fatal(err)
		}
	}
	if gitTest(t, f.repos["api"], "branch", "--show-current") != "other-local-feature" {
		t.Fatal("source branch changed")
	}
}
func TestFetchUsesNewRemoteCommitAndRepairsOriginHead(t *testing.T) {
	f := newFixture(t)
	clone := filepath.Join(f.root, "second-clone")
	gitTest(t, f.root, "clone", filepath.Join(f.root, "api.git"), clone)
	gitTest(t, clone, "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "remote change")
	gitTest(t, clone, "push", "origin", "release")
	expected := gitTest(t, clone, "rev-parse", "HEAD")
	gitTest(t, f.repos["api"], "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	f.mustCLI("start", "feat/shared", "api", "web", "--no-open", "--fetch")
	m := f.manifest()
	if m.Repos[0].BaseCommit != expected {
		t.Fatal("did not fetch latest remote commit")
	}
	if gitTest(t, f.repos["api"], "symbolic-ref", "--short", "refs/remotes/origin/HEAD") != "origin/release" {
		t.Fatal("origin HEAD not repaired")
	}
}
func TestStartUsesLocalBaseWithoutNetwork(t *testing.T) {
	f := newFixture(t)
	expected := gitTest(t, f.repos["api"], "rev-parse", "origin/release")
	gitTest(t, f.repos["api"], "remote", "set-url", "origin", filepath.Join(f.root, "unavailable.git"))
	m := f.start()
	if m.Repos[0].BaseCommit != expected {
		t.Fatal("did not pin local remote-tracking base")
	}
	if f.count("git", "fetch") != 0 || f.count("git", "ls-remote") != 0 {
		t.Fatal("default start accessed the network")
	}
}

func TestStartMissingLocalBaseSuggestsFetch(t *testing.T) {
	for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/release"} {
		t.Run(ref, func(t *testing.T) {
			f := newFixture(t)
			gitTest(t, f.repos["api"], "update-ref", "--no-deref", "-d", ref)
			code, out := f.cli("start", "feat/shared", "api", "web", "--no-open")
			if code != 1 || !strings.Contains(out, "--fetch") {
				t.Fatalf("expected actionable missing-base error: %d %s", code, out)
			}
			if f.count("workmux", "add") != 0 || f.count("git", "ls-remote") != 0 || f.count("git", "fetch") != 0 {
				t.Fatal("missing base caused provisioning or network access")
			}
		})
	}
}
func TestRepeatedStartKeepsOriginalBase(t *testing.T) {
	f := newFixture(t)
	first := f.start()
	second := f.start()
	if first.Repos[0].BaseCommit != second.Repos[0].BaseCommit || f.count("workmux", "add") != 2 {
		t.Fatal("reprovisioned")
	}
}
func TestDryRunDoesNotMutateOrFetch(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web", "--dry-run")
	if exists(filepath.Join(f.root, ".wmm")) {
		t.Fatal("dry-run wrote state")
	}
	for _, args := range f.calls {
		if args[0] != "git" || slices.Contains(args, "fetch") || slices.Contains(args, "ls-remote") {
			t.Fatal(args)
		}
	}
}
func TestUnknownFlagBeforeAnyDependency(t *testing.T) {
	f := newFixture(t)
	code, out := f.cli("start", "feat/shared", "api", "--typo")
	if code != 2 || !strings.Contains(out, "--dry-run") || len(f.calls) != 0 {
		t.Fatalf("%d %s %v", code, out, f.calls)
	}
}
func TestConflictingBranchPreflight(t *testing.T) {
	f := newFixture(t)
	gitTest(t, f.repos["web"], "branch", "feat/shared")
	code, _ := f.cli("start", "feat/shared", "api", "web", "--no-open")
	if code != 1 || f.count("workmux", "add") != 0 {
		t.Fatal("conflict not caught before provisioning")
	}
}
func TestPartialFailureRetryPreservesWork(t *testing.T) {
	f := newFixture(t)
	f.failAlias = "web"
	code, _ := f.cli("start", "feat/shared", "api", "web", "--no-open")
	if code != 1 {
		t.Fatal(code)
	}
	partial := f.manifest()
	if !partial.Repos[0].Ready || partial.Repos[1].Ready {
		t.Fatal(partial)
	}
	marker := filepath.Join(partial.Repos[0].Path, "keep.txt")
	if err := os.WriteFile(marker, []byte("my work"), 0644); err != nil {
		t.Fatal(err)
	}
	f.failAlias = ""
	f.start()
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "my work" {
		t.Fatal("work lost")
	}
}
func TestHookFailureAfterCreationRetriesProvisioning(t *testing.T) {
	f := newFixture(t)
	f.failAlias = "web"
	f.failAfterCreate = true
	if code, _ := f.cli("start", "feat/shared", "api", "web", "--no-open"); code != 1 {
		t.Fatal(code)
	}
	partial := f.manifest()
	if partial.Repos[1].Path == "" {
		t.Fatal("incomplete worktree was not recorded")
	}
	f.failAlias = ""
	code, out := f.cli("start", "feat/shared", "api", "web", "--no-open")
	if code != 0 {
		t.Fatal(code, out)
	}
	if !f.manifest().Repos[1].Ready || f.count("workmux", "add") != 3 {
		t.Fatal("did not retry incomplete setup while preserving completed repositories")
	}
}

func TestIncompleteProvisioningPreservesChanges(t *testing.T) {
	for _, kind := range []string{"tracked", "untracked", "ignored", "commit"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.failAlias, f.failAfterCreate = "web", true
			f.cli("start", "feat/shared", "api", "web", "--no-open")
			r := f.manifest().Repos[1]
			path, err := f.a.worktreeFor(r, "feat/shared")
			if err != nil || path == "" {
				t.Fatal(path, err)
			}
			file := filepath.Join(path, "keep.txt")
			switch kind {
			case "tracked":
				file = filepath.Join(path, "file.txt")
			case "ignored":
				gitTest(t, path, "config", "core.excludesFile", filepath.Join(f.root, "ignore"))
				if err := os.WriteFile(filepath.Join(f.root, "ignore"), []byte("keep.txt\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(file, []byte("preserve me"), 0644); err != nil {
				t.Fatal(err)
			}
			if kind == "commit" {
				gitTest(t, path, "add", ".")
				gitTest(t, path, "commit", "-m", "keep work")
			}
			f.failAlias = ""
			code, out := f.cli("start", "feat/shared", "api", "web", "--no-open")
			if code != 1 || !strings.Contains(out, "preserved") || f.count("workmux", "remove") != 0 {
				t.Fatal(code, out)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "preserve me" {
				t.Fatal("lost changes", err)
			}
		})
	}
}

func TestRetryRecoversInterruptedProvisioning(t *testing.T) {
	for _, stage := range []string{"legacy-worktree", "branch-only"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			f.failAlias, f.failAfterCreate = "web", true
			f.cli("start", "feat/shared", "api", "web", "--no-open")
			m := f.manifest()
			r := &m.Repos[1]
			if stage == "branch-only" {
				gitTest(t, r.Source, "worktree", "remove", r.Path)
			} else {
				r.Path, r.Attempted = "", false
			}
			if err := save(f.config(), m); err != nil {
				t.Fatal(err)
			}
			f.failAlias = ""
			f.start()
			if !f.manifest().Repos[1].Ready {
				t.Fatal("incomplete repository was not recovered")
			}
		})
	}
}
func TestStatusTracksCommitsAndUntrackedFiles(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	path := m.Repos[0].Path
	gitTest(t, path, "commit", "--allow-empty", "-m", "change")
	if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("untracked"), 0644); err != nil {
		t.Fatal(err)
	}
	out := f.mustCLI("status", "feat/shared", "--fields", "repo,state,commits,path")
	if !strings.Contains(out, `"api","modified",1,`) {
		t.Fatal(out)
	}
}
func TestReviewGroupedAndIdempotentWindows(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	f.mustCLI("review", "feat/shared", "--prepare-pr")
	f.mustCLI("review", "feat/shared", "--prepare-pr")
	if f.count("workmux", "open") != 3 || len(f.windows) != 1 {
		t.Fatal(f.windows)
	}
	for _, args := range f.calls {
		if len(args) > 1 && args[0] == "workmux" && args[1] == "open" && strings.HasPrefix(flagValue(args, "--target-name"), "review-") {
			prompt := flagValue(args, "--prompt")
			if !strings.Contains(prompt, "Do not push or publish") || !strings.Contains(prompt, "staged, unstaged, and untracked") {
				t.Fatal(args)
			}
		}
	}

}
func TestStartDuringReviewReplacesChangeWindowWithBuilder(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.mustCLI("review", "feat/shared")
	review := f.windows["review"]
	position := slices.Index(f.order, review)
	f.mustCLI("start", "feat/shared", "api", "web")
	build := f.windows["build"]
	if build == "" || f.windows["review"] != "" || slices.Index(f.order, build) != position {
		t.Fatal(f.windows, f.order)
	}
	if phase := f.manifest().Phase; phase != "implementation" {
		t.Fatal(phase)
	}
}
func TestBuilderSingleWindow(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web", "--prompt", "Fix $(touch SHOULD_NOT_EXIST)")
	if f.count("tmux", "new-session") != 1 || f.windows["build"] == "" {
		t.Fatal(f.windows)
	}
	if exists(filepath.Join(f.root, "SHOULD_NOT_EXIST")) {
		t.Fatal("shell expansion")
	}
	f.mustCLI("start", "feat/shared", "api", "web")
	if f.count("workmux", "open") != 1 {
		t.Fatal("builder was not delegated/idempotent")
	}
	for _, args := range f.calls {
		if slices.Contains(args, "--agent") || slices.Contains(args, "claude") {
			t.Fatal(args)
		}
	}
}
func TestSwitchedBranchNotReused(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	gitTest(t, m.Repos[0].Path, "switch", "-c", "oops")
	if code, _ := f.cli("review", "feat/shared"); code != 1 {
		t.Fatal(code)
	}
	if code, _ := f.cli("start", "feat/shared", "api", "web", "--no-open"); code != 1 {
		t.Fatal(code)
	}
}
func TestInvalidInputsAndUniqueIDs(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"start", "feat/shared", "api", "api", "--dry-run"}, {"start", "../../oops", "api", "--dry-run"}, {"start", "feat/x", "unknown", "--dry-run"}, {"status", "--fields", "secret"}} {
		if code, _ := f.cli(args...); code != 2 {
			t.Fatal(args, code)
		}
	}
	if changeID("feat/a-b") == changeID("feat-a/b") {
		t.Fatal("change ID collision")
	}
}
func TestInitIdempotentAndValid(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, "other.toml")
	f.mustCLI("init", "--config", path)
	first, _ := os.ReadFile(path)
	f.mustCLI("init", "--config", path)
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("overwrote configuration")
	}
	if _, err := f.a.discover(path); err != nil {
		t.Fatal(err)
	}
}
func TestEmptyStatus(t *testing.T) {
	f := newFixture(t)
	if out := f.mustCLI("status"); !strings.Contains(out, "changes[0]") {
		t.Fatal(out)
	}
}
func TestLockExcludesConcurrentMutation(t *testing.T) {
	f := newFixture(t)
	unlock, err := lock(f.config())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if code, out := f.cli("start", "feat/shared", "api", "web", "--no-open"); code != 1 || !strings.Contains(out, "Another wmm mutation") {
		t.Fatal(code, out)
	}
}
func TestRemovedAgentConfigurationExplainsMigration(t *testing.T) {
	f := newFixture(t)
	for _, setting := range []string{"build_command = ['claude']", "review_agent = 'claude'"} {
		if err := os.WriteFile(filepath.Join(f.root, "wmm.toml"), []byte(setting+"\n[repos]\napi = './api repo'\n"), 0644); err != nil {
			t.Fatal(err)
		}
		code, out := f.cli("start", "feat/shared", "api")
		if code != 2 || !strings.Contains(out, "no longer supported") || !strings.Contains(out, ".workmux.yaml") || len(f.calls) != 0 {
			t.Fatal(code, out)
		}
	}
}

func TestDelegationPreservesOriginalWorkmuxConfig(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.root, ".workmux.yml")
	data := []byte("agent: custom-agent\nwindow_prefix: custom-\n")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	f.mustCLI("start", "feat/shared", "api", "web")
	delete(f.windows, "build")
	f.mustCLI("review", "feat/shared")
	for _, args := range f.calls {
		if len(args) < 2 || args[0] != "workmux" || args[1] != "open" {
			continue
		}
		if strings.HasPrefix(flagValue(args, "--target-name"), "build-") {
			if flagValue(args, "--config") != path {
				t.Fatal(args)
			}
		} else if slices.Contains(args, "--config") {
			t.Fatal("review config overridden", args)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("workmux config changed")
	}
	if exists(filepath.Join(workspace(f.config(), "feat/shared"), "review.workmux.yaml")) {
		t.Fatal("generated duplicate config")
	}
}
func TestHelpAndVersionWithoutConfig(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"start", "--help"}, {"--version"}} {
		var out bytes.Buffer
		a := &app{cwd: t.TempDir(), out: &out, err: io.Discard, run: func(string, ...string) (string, error) { t.Fatal("help ran dependency"); return "", nil }}
		if code := a.invoke(args); code != 0 || out.Len() == 0 {
			t.Fatal(code, out.String())
		}
	}
}

func TestReopenRepairsMissingWorkspaceLink(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	link := filepath.Join(workspace(f.config(), m.Branch), "web")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	f.start()
	if _, err := os.Readlink(link); err != nil {
		t.Fatal(err)
	}
	if f.count("workmux", "add") != 2 {
		t.Fatal("recreated existing worktrees")
	}
}

func TestCurrentSessionWithoutGeneratedContext(t *testing.T) {
	f := newFixture(t)
	t.Setenv("TMUX", "test,1,0")
	f.windows["shell"] = "@0"
	original := f.a.run
	f.a.run = func(dir string, args ...string) (string, error) {
		if slices.Equal(args, []string{"tmux", "display-message", "-p", "#{session_name}"}) {
			return "current", nil
		}
		if len(args) > 1 && args[0] == "tmux" && args[1] == "list-windows" && flagValue(args, "-t") != "=current" {
			return "", errors.New("no session")
		}
		return original(dir, args...)
	}
	f.mustCLI("start", "feat/shared", "api", "web")
	if f.manifest().Session != "current" || f.count("tmux", "new-session") != 0 {
		t.Error("did not reuse current session")
	}
	delete(f.windows, "build")
	f.mustCLI("review", "feat/shared")
	for _, args := range f.calls {
		if len(args) > 1 && args[0] == "workmux" && args[1] == "open" {
			if flagValue(args, "--parent-session") != "current" || slices.Contains(args, "--prompt-file") || slices.Contains(args, "--prompt") {
				t.Error(args)
			}
		}
	}
	dir := workspace(f.config(), "feat/shared")
	for _, name := range []string{"BRIEF.md", "build-prompt.md", "review-api-prompt.md", "review-web-prompt.md"} {
		if exists(filepath.Join(dir, name)) {
			t.Errorf("generated %s", name)
		}
	}
}

func TestSharedSessionIgnoresOtherChangesAndOldPrompts(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	m.Session = "current"
	m.Objective = "Old automatic prompt"
	if err := save(f.config(), m); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "test,1,0")
	f.windows["review-other"] = "@99"
	f.owners["@99"] = "/another/workspace"
	f.mustCLI("start", "feat/shared", "api", "web")
	f.mustCLI("start", "feat/shared", "api", "web")
	if f.count("workmux", "open") != 1 {
		t.Fatal("builder not reused")
	}
	for _, args := range f.calls {
		if slices.Contains(args, "--prompt") || slices.Contains(args, "--prompt-file") {
			t.Fatal("replayed old prompt", args)
		}
	}
	delete(f.windows, "build")
	f.mustCLI("review", "feat/shared")
	if f.count("workmux", "open") != 3 {
		t.Fatal("reviewers not opened")
	}
}

func TestReviewInsideTmuxReplacesImplementationWindow(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	position := slices.Index(f.order, f.windows["build"])
	m := f.manifest()
	m.Session = "current"
	if err := save(f.config(), m); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "test,1,0")
	f.mustCLI("review", "feat/shared")
	f.mustCLI("review", "feat/shared")
	if f.windows["build"] != "" || len(f.windows) != 1 || slices.Index(f.order, f.windows["review"]) != position || len(f.panes) != 3 || f.count("workmux", "open") != 3 {
		t.Fatal(f.windows, f.panes)
	}
	f.mustCLI("start", "feat/shared", "api", "web")
	if f.windows["review"] != "" || f.windows["build"] == "" || len(f.windows) != 1 {
		t.Fatal("builder did not replace the review window", f.windows)
	}
}

func TestReviewRetriesInterruptedPaneTransfer(t *testing.T) {
	f := newFixture(t)
	f.start()
	original := f.a.run
	failed := false
	f.a.run = func(dir string, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "tmux" && args[1] == "join-pane" && !failed {
			failed = true
			return "", errors.New("interrupted")
		}
		return original(dir, args...)
	}
	if code, _ := f.cli("review", "feat/shared"); code != 1 {
		t.Fatal("expected join failure")
	}
	f.mustCLI("review", "feat/shared")
	if f.count("workmux", "open") != 2 || len(f.windows) != 1 || len(f.panes) != 2 {
		t.Fatal(f.windows, f.panes)
	}
}
func TestDebugOpensChangeWindowInRootDirectory(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("debug", "feat/shared")
	if f.windows["debug"] == "" || len(f.windows) != 1 {
		t.Fatal(f.windows)
	}
	opened := f.calls[slices.IndexFunc(f.calls, func(args []string) bool {
		return len(args) > 1 && args[0] == "tmux" && (args[1] == "new-session" || args[1] == "new-window")
	})]
	if flagValue(opened, "-c") != f.root || flagValue(opened, "-n") != "\uead8 feat/shared" {
		t.Fatal(opened)
	}
	m := f.manifest()
	if m.Phase != "debug" || len(m.Repos) != 0 {
		t.Fatal(m)
	}
}
func TestDebugRefusesChangeWithWorktrees(t *testing.T) {
	f := newFixture(t)
	f.start()
	if code, _ := f.cli("debug", "feat/shared"); code != 1 {
		t.Fatal(code)
	}
	if m := f.manifest(); len(m.Repos) != 2 || len(f.windows) != 0 {
		t.Fatal(m.Phase, f.windows)
	}
}
func TestRepeatedDebugReusesChangeWindow(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("debug", "feat/shared")
	window := f.windows["debug"]
	f.mustCLI("debug", "feat/shared")
	if len(f.windows) != 1 || f.windows["debug"] != window || f.count("tmux", "new-session")+f.count("tmux", "new-window") != 1 {
		t.Fatal(f.windows)
	}
}
func TestStartAfterDebugReplacesChangeWindowWithBuilder(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("debug", "feat/shared")
	position := slices.Index(f.order, f.windows["debug"])
	f.mustCLI("start", "feat/shared", "api", "web")
	build := f.windows["build"]
	if build == "" || f.windows["debug"] != "" || slices.Index(f.order, build) != position {
		t.Fatal(f.windows, f.order)
	}
	if m := f.manifest(); m.Phase != "implementation" || len(m.Repos) != 2 {
		t.Fatal(m)
	}
}
func TestSingleRepositoryBuilderRunsInItsWorktree(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api")
	m := f.manifest()
	var opened, renamed []string
	for _, args := range f.calls {
		if len(args) > 1 && args[0] == "workmux" && args[1] == "open" {
			opened = args
		}
		if len(args) > 1 && args[0] == "tmux" && args[1] == "rename-window" {
			renamed = args
		}
	}
	if opened == nil || opened[2] != m.Repos[0].Handle || f.windows["build"] == "" {
		t.Fatal(opened, f.windows)
	}
	if renamed[len(renamed)-1] != " api:feat/shared" {
		t.Fatal(renamed)
	}
}
func TestMultiRepositoryBuilderWindowName(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	for _, args := range f.calls {
		if len(args) > 1 && args[0] == "tmux" && args[1] == "rename-window" && args[len(args)-1] != " feat/shared" {
			t.Fatal(args)
		}
	}
}
func TestReviewReplacesImplementationWindow(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	position := slices.Index(f.order, f.windows["build"])
	f.mustCLI("review", "feat/shared")
	review := f.windows["review"]
	if review == "" || f.windows["build"] != "" || len(f.windows) != 1 || slices.Index(f.order, review) != position {
		t.Fatal(f.windows, f.order)
	}
	renamed := false
	for _, args := range f.calls {
		if slices.Equal(args, []string{"tmux", "rename-window", "-t", review, " feat/shared"}) {
			renamed = true
		}
	}
	if !renamed || f.manifest().Phase != "review" {
		t.Fatal("review window not named", f.manifest().Phase)
	}
}
func (f *fixture) lastLayout() string {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if args := f.calls[i]; len(args) > 1 && args[0] == "tmux" && args[1] == "select-layout" {
			return args[len(args)-1]
		}
	}
	return ""
}
func TestReviewersSideBySideUpToFourPanes(t *testing.T) {
	f := newFixture(t)
	f.panesPerWindow = 2
	f.start()
	f.mustCLI("review", "feat/shared")
	if layout := f.lastLayout(); layout != "even-horizontal" {
		t.Fatal(layout)
	}
}
func TestReviewersTiledFromFivePanes(t *testing.T) {
	f := newFixture(t)
	f.panesPerWindow = 3
	f.start()
	f.mustCLI("review", "feat/shared")
	if layout := f.lastLayout(); layout != "tiled" {
		t.Fatal(layout)
	}
}
func TestReviewRefusesChangeInDebug(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("debug", "feat/shared")
	if code, _ := f.cli("review", "feat/shared"); code != 1 {
		t.Fatal(code)
	}
	if f.windows["debug"] == "" || f.count("workmux", "open") != 0 {
		t.Fatal(f.windows)
	}
}
func (f *fixture) reviewerConfigs() []string {
	var configs []string
	for _, args := range f.calls {
		if len(args) > 1 && args[0] == "workmux" && args[1] == "open" && strings.HasPrefix(flagValue(args, "--target-name"), "review-") {
			configs = append(configs, flagValue(args, "--config"))
		}
	}
	return configs
}
func TestReviewersUseWorkmuxConfigFromFlag(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.mustCLI("review", "feat/shared", "--workmux-config", "review.yaml")
	want := filepath.Join(f.root, "review.yaml")
	if configs := f.reviewerConfigs(); !slices.Equal(configs, []string{want, want}) {
		t.Fatal(configs)
	}
}
func TestReviewersUseWorkmuxConfigFromUserConfig(t *testing.T) {
	f := newFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if err := os.MkdirAll(filepath.Join(home, ".config", "wmm"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "wmm", "config.toml"), []byte("review_workmux_config = '~/review.yaml'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.mustCLI("review", "feat/shared")
	want := filepath.Join(home, "review.yaml")
	if configs := f.reviewerConfigs(); !slices.Equal(configs, []string{want, want}) {
		t.Fatal(configs)
	}
}
func TestReviewWorkmuxConfigFlagOverridesUserConfig(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wmm"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wmm", "config.toml"), []byte("review_workmux_config = '/ignored.yaml'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.mustCLI("review", "feat/shared", "--workmux-config", "/chosen.yaml")
	if configs := f.reviewerConfigs(); !slices.Equal(configs, []string{"/chosen.yaml", "/chosen.yaml"}) {
		t.Fatal(configs)
	}
}
func TestSingleRepositoryReviewerLayoutFollowsPaneCount(t *testing.T) {
	f := newFixture(t)
	f.panesPerWindow = 5
	f.mustCLI("start", "feat/shared", "api", "--no-open")
	f.mustCLI("review", "feat/shared")
	if layout := f.lastLayout(); layout != "tiled" {
		t.Fatal(layout)
	}
}
func TestRepeatedStartFocusesBuilder(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	f.calls = nil
	f.mustCLI("start", "feat/shared", "api", "web")
	if f.count("tmux", "select-window") != 1 || f.count("workmux", "open") != 0 {
		t.Fatal(f.calls)
	}
}
func TestReviewAfterUnopenedStartReplacesDebugWindow(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("debug", "feat/shared")
	position := slices.Index(f.order, f.windows["debug"])
	f.mustCLI("start", "feat/shared", "api", "web", "--no-open")
	f.mustCLI("review", "feat/shared")
	review := f.windows["review"]
	if review == "" || len(f.windows) != 1 || slices.Index(f.order, review) != position {
		t.Fatal(f.windows, f.order)
	}
}
func (f *fixture) lastCall() []string {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if args := f.calls[i]; args[0] == "tmux" {
			return args
		}
	}
	return nil
}
func TestReplacedWindowClosesAfterFocus(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("debug", "feat/shared")
	debug := f.windows["debug"]
	f.mustCLI("start", "feat/shared", "api", "web")
	if last := f.lastCall(); !slices.Equal(last, []string{"tmux", "kill-window", "-t", debug}) {
		t.Fatal(last)
	}
	build := f.windows["build"]
	f.mustCLI("review", "feat/shared")
	if last := f.lastCall(); !slices.Equal(last, []string{"tmux", "kill-window", "-t", build}) {
		t.Fatal(last)
	}
}
