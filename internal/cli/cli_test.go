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
	nextWindow      int
	failAlias       string
	failAfterCreate bool
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
	root, err := canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, repos: map[string]string{}, windows: map[string]string{}}
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
		gitTest(f.t, cwd, "worktree", "add", "-b", args[2], path, flagValue(args, "--base"))
		if alias == f.failAlias {
			return "", errors.New("simulated hook failure after creation")
		}
		data, _ := json.Marshal(map[string]any{"schema_version": 1, "branch": args[2], "worktree_path": path})
		return string(data), nil
	}
	if len(args) >= 2 && args[0] == "workmux" && args[1] == "open" {
		f.nextWindow++
		f.windows["wmm-"+flagValue(args, "--target-name")] = fmt.Sprintf("@%d", f.nextWindow)
		return "", nil
	}
	if len(args) >= 2 && args[0] == "tmux" {
		switch args[1] {
		case "list-windows":
			if len(f.windows) == 0 {
				return "", errors.New("no session")
			}
			var out strings.Builder
			for _, name := range keys(f.windows) {
				fmt.Fprintf(&out, "%s\t%s\n", f.windows[name], name)
			}
			return out.String(), nil
		case "new-session", "new-window":
			f.nextWindow++
			id := fmt.Sprintf("@%d", f.nextWindow)
			f.windows[flagValue(args, "-n")] = id
			return id, nil
		case "kill-window":
			for name, id := range f.windows {
				if id == flagValue(args, "-t") {
					delete(f.windows, name)
				}
			}
		}
		return "", nil
	}
	return execute(cwd, args...)
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
	m := f.start()
	if m.Repos[0].BaseCommit != expected {
		t.Fatal("did not fetch latest remote commit")
	}
	if gitTest(t, f.repos["api"], "symbolic-ref", "--short", "refs/remotes/origin/HEAD") != "origin/release" {
		t.Fatal("origin HEAD not repaired")
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
func TestHookFailureAfterCreationIsNotSilentlyAdopted(t *testing.T) {
	f := newFixture(t)
	f.failAlias = "web"
	f.failAfterCreate = true
	if code, _ := f.cli("start", "feat/shared", "api", "web", "--no-open"); code != 1 {
		t.Fatal(code)
	}
	f.failAlias = ""
	code, out := f.cli("start", "feat/shared", "api", "web", "--no-open")
	if code != 1 || !strings.Contains(out, "Incomplete provisioning") {
		t.Fatal(code, out)
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
func TestReviewGuardAndIdempotentWindows(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.windows["build"] = "@1"
	if code, _ := f.cli("review", "feat/shared"); code != 1 || f.count("workmux", "open") != 0 {
		t.Fatal("review bypassed builder")
	}
	delete(f.windows, "build")
	f.mustCLI("review", "feat/shared", "--prepare-pr")
	f.mustCLI("review", "feat/shared", "--prepare-pr")
	if f.count("workmux", "open") != 2 || len(f.windows) != 2 {
		t.Fatal(f.windows)
	}
	data, err := os.ReadFile(filepath.Join(workspace(f.config(), "feat/shared"), "review-api-prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Do not push or publish") || !strings.Contains(string(data), "staged, unstaged, and untracked") {
		t.Fatal(string(data))
	}
}
func TestBuilderRefusesLiveReviewers(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.windows["wmm-review-api"] = "@1"
	if code, _ := f.cli("start", "feat/shared", "api", "web"); code != 1 {
		t.Fatal(code)
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
	for _, args := range f.calls {
		if args[0] == "tmux" && args[1] == "new-session" {
			if !strings.Contains(args[len(args)-1], "--add-dir") {
				t.Fatal(args)
			}
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
func TestShellQuoteRoundTrip(t *testing.T) {
	input := []string{"spaces here", "quote'and\"double", "$(touch /tmp/do-not-create-wmm)", "line\nbreak"}
	command := "printf '%s\\0' " + shellJoin(input)
	output, err := exec.Command("sh", "-c", command).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != strings.Join(input, "\x00")+"\x00" {
		t.Fatal(string(output))
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
