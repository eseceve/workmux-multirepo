package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRemoveWholeChangeAndRepeat(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	m := f.manifest()
	f.windows["unrelated"] = "@999"
	f.mustCLI("remove", m.Branch)
	if exists(workspace(f.config(), m.Branch)) || len(f.windows) != 1 || f.windows["unrelated"] != "@999" {
		t.Fatal("workspace or owned windows remain, or unrelated window removed", f.windows)
	}
	for _, r := range m.Repos {
		if exists(r.Path) {
			t.Fatal("worktree remains", r.Path)
		}
		if _, err := execute(r.Source, "git", "show-ref", "--verify", "refs/heads/"+m.Branch); err == nil {
			t.Fatal("branch remains")
		}
	}
	f.mustCLI("remove", m.Branch)
}

func TestRemovePreflightPreservesEntireGroup(t *testing.T) {
	for _, kind := range []string{"dirty", "commit", "workspace", "switched"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			m := f.start()
			switch kind {
			case "dirty":
				if err := os.WriteFile(filepath.Join(m.Repos[1].Path, "keep.txt"), []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
			case "commit":
				gitTest(t, m.Repos[1].Path, "commit", "--allow-empty", "-m", "keep commit")
			case "workspace":
				if err := os.WriteFile(filepath.Join(workspace(f.config(), m.Branch), "notes.md"), []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
			case "switched":
				gitTest(t, m.Repos[1].Path, "switch", "-c", "other")
			}
			code, out := f.cli("remove", m.Branch)
			if code != 1 || f.count("workmux", "remove") != 0 || f.count("tmux", "kill-window") != 0 {
				t.Fatal(code, out)
			}
			for _, r := range m.Repos {
				if !exists(r.Path) {
					t.Fatal("partial removal on preflight failure")
				}
			}
		})
	}
}

func TestRemoveKeepBranches(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	gitTest(t, m.Repos[1].Path, "commit", "--allow-empty", "-m", "keep commit")
	expected := gitTest(t, m.Repos[1].Path, "rev-parse", "HEAD")
	f.mustCLI("remove", m.Branch, "--keep-branch")
	if actual := gitTest(t, m.Repos[1].Source, "rev-parse", m.Branch); actual != expected {
		t.Fatal("commit lost")
	}
}

func TestRemoveForceAndDryRun(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	if err := os.WriteFile(filepath.Join(m.Repos[1].Path, "discard.txt"), []byte("discard"), 0644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, m.Repos[1].Path, "commit", "--allow-empty", "-m", "discard commit")
	f.mustCLI("remove", m.Branch, "--force", "--dry-run")
	if f.count("workmux", "remove") != 0 || f.manifest().Phase != "ready" {
		t.Fatal("dry-run mutated state")
	}
	f.mustCLI("remove", m.Branch, "--force")
	if exists(workspace(f.config(), m.Branch)) {
		t.Fatal("workspace remains")
	}
}

func TestRemoveResumesPartialFailure(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	f.failRemoveAlias = "web"
	if code, _ := f.cli("remove", m.Branch); code != 1 {
		t.Fatal("expected removal failure")
	}
	partial := f.manifest()
	if !partial.Repos[0].Removed || partial.Repos[1].Removed || partial.Phase != "removing" {
		t.Fatal(partial)
	}
	if code, out := f.cli("start", m.Branch, "api", "web"); code != 1 || !strings.Contains(out, "Removal is incomplete") {
		t.Fatal(code, out)
	}
	f.failRemoveAlias = ""
	f.mustCLI("remove", m.Branch)
	if exists(workspace(f.config(), m.Branch)) || f.count("workmux", "remove") != 3 {
		t.Fatal("removal was not resumed")
	}
}

func TestRemoveIncompleteStart(t *testing.T) {
	f := newFixture(t)
	f.failAlias, f.failAfterCreate = "web", true
	f.cli("start", "feat/shared", "api", "web", "--no-open")
	f.mustCLI("remove", "feat/shared")
	if exists(workspace(f.config(), "feat/shared")) {
		t.Fatal("incomplete workspace remains")
	}
}
func TestRemoveChangeInDebug(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("debug", "feat/shared")
	f.mustCLI("remove", "feat/shared")
	if len(f.windows) != 0 || exists(workspace(f.config(), "feat/shared")) {
		t.Fatal(f.windows)
	}
}
func TestRemoveChangeInDebugWithoutWorkmux(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("debug", "feat/shared")
	f.a.lookup = func(name string) (string, error) {
		if name == "workmux" {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	}
	f.mustCLI("remove", "feat/shared")
	if len(f.windows) != 0 || exists(workspace(f.config(), "feat/shared")) {
		t.Fatal(f.windows)
	}
}
func TestRemoveInfersChangeFromWorkingDirectory(t *testing.T) {
	for _, place := range []string{"worktree", "subdirectory", "workspace"} {
		t.Run(place, func(t *testing.T) {
			f := newFixture(t)
			m := f.start()
			switch place {
			case "worktree":
				f.a.cwd = m.Repos[1].Path
			case "subdirectory":
				dir := filepath.Join(m.Repos[1].Path, "nested")
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				f.a.cwd = dir
			case "workspace":
				f.a.cwd = workspace(f.config(), m.Branch)
			}
			f.mustCLI("remove", "-f")
			if exists(workspace(f.config(), m.Branch)) {
				t.Fatal("workspace remains")
			}
		})
	}
}

func TestRemoveWithoutBranchOutsideAChangeRemovesNothing(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	code, out := f.cli("remove")
	if code == 0 || !strings.Contains(out, "No change contains") || f.count("workmux", "remove") != 0 || !exists(workspace(f.config(), m.Branch)) {
		t.Fatal(code, out)
	}
}

func (f *fixture) changeWindowPane(branch string) string {
	f.t.Helper()
	dir := workspace(f.config(), branch)
	for _, pane := range keys(f.panes) {
		if f.owners[f.panes[pane]] == dir {
			return pane
		}
	}
	f.t.Fatal("no pane in the change window")
	return ""
}

func (f *fixture) lastCallOf(command, sub string) []string {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if len(f.calls[i]) > 1 && f.calls[i][0] == command && f.calls[i][1] == sub {
			return f.calls[i]
		}
	}
	return nil
}

func TestRemoveFromChangeWindowFinishesInBackground(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	t.Setenv("TMUX_PANE", f.changeWindowPane("feat/shared"))
	phase, killed := f.manifest().Phase, f.count("tmux", "kill-window")
	out := f.mustCLI("remove", "feat/shared", "--force")
	if f.count("workmux", "remove") != 0 || f.count("tmux", "kill-window") != killed || f.manifest().Phase != phase {
		t.Fatal("removal ran inside the window it closes", out)
	}
	shell := f.lastCallOf("tmux", "run-shell")
	if !slices.Contains(shell, "-b") {
		t.Fatal("removal was not handed to the tmux server", shell)
	}
	command := shell[len(shell)-1]
	for _, part := range []string{detachedRemovalEnv + "=1", "remove 'feat/shared'", "--config '" + f.config().Path + "'", "--force"} {
		if !strings.Contains(command, part) {
			t.Fatal("background command misses "+part, command)
		}
	}
	if !strings.Contains(out, "removing in background") {
		t.Fatal(out)
	}
}

func TestRemoveRelaunchedFromChangeWindowRemovesEverything(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	t.Setenv("TMUX_PANE", f.changeWindowPane("feat/shared"))
	t.Setenv(detachedRemovalEnv, "1")
	f.mustCLI("remove", "feat/shared", "--force")
	if f.count("tmux", "run-shell") != 0 || exists(workspace(f.config(), "feat/shared")) || len(f.windows) != 0 {
		t.Fatal("relaunched removal did not finish", f.windows)
	}
}

func TestRemoveFromAnotherWindowStaysInForeground(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web")
	f.panes["%99.0"] = "@99"
	t.Setenv("TMUX_PANE", "%99.0")
	f.mustCLI("remove", "feat/shared", "--force")
	if f.count("tmux", "run-shell") != 0 || exists(workspace(f.config(), "feat/shared")) {
		t.Fatal("removal from an unrelated window did not run in place")
	}
}

func TestRelaunchedRemovalWaitsForTheLauncherLock(t *testing.T) {
	f := newFixture(t)
	f.mustCLI("start", "feat/shared", "api", "web", "--no-open")
	unlock, err := lock(f.config())
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(200*time.Millisecond, unlock)
	t.Setenv(detachedRemovalEnv, "1")
	f.mustCLI("remove", "feat/shared", "--force")
	if exists(workspace(f.config(), "feat/shared")) {
		t.Fatal("relaunched removal gave up on the lock")
	}
}
