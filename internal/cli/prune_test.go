package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (f *fixture) loseWorktrees(m *manifest) {
	f.t.Helper()
	for _, r := range m.Repos {
		gitTest(f.t, r.Source, "worktree", "remove", "--force", r.Path)
	}
}

func TestPruneFinishesIncompleteRemovals(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	f.failRemoveAlias = "web"
	if code, _ := f.cli("remove", m.Branch); code != 1 {
		t.Fatal("expected removal failure")
	}
	f.failRemoveAlias = ""
	out := f.mustCLI("prune")
	if exists(workspace(f.config(), m.Branch)) || exists(m.Repos[1].Path) || !strings.Contains(out, "removed") {
		t.Fatal("incomplete removal was not finished", out)
	}
}

func TestPruneRemovesChangesWhoseWorktreesAreGone(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	f.mustCLI("start", "feat/alive", "api", "--no-open")
	f.mustCLI("debug", "feat/investigating")
	f.loseWorktrees(m)
	f.mustCLI("prune")
	if exists(workspace(f.config(), m.Branch)) {
		t.Fatal("abandoned workspace remains")
	}
	for _, branch := range []string{"feat/alive", "feat/investigating"} {
		if !exists(workspace(f.config(), branch)) {
			t.Fatal("pruned a live change", branch)
		}
	}
	if _, err := execute(m.Repos[0].Source, "git", "show-ref", "--verify", "refs/heads/"+m.Branch); err == nil {
		t.Fatal("merged branch remains")
	}
}

func TestPruneDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	f.loseWorktrees(m)
	out := f.mustCLI("prune", "--dry-run")
	if !exists(workspace(f.config(), m.Branch)) || !strings.Contains(out, m.Branch) {
		t.Fatal("dry-run removed the change or did not list it", out)
	}
}

func TestPruneKeepsWhatWouldLoseWorkUnlessForced(t *testing.T) {
	for _, kind := range []string{"workspace", "commit", "unregistered"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			m := f.start()
			switch kind {
			case "workspace":
				f.loseWorktrees(m)
				if err := os.WriteFile(filepath.Join(workspace(f.config(), m.Branch), "notes.md"), []byte("keep"), 0644); err != nil {
					t.Fatal(err)
				}
			case "commit":
				gitTest(t, m.Repos[1].Path, "commit", "--allow-empty", "-m", "keep commit")
				f.loseWorktrees(m)
			case "unregistered":
				gitTest(t, m.Repos[0].Source, "worktree", "remove", "--force", m.Repos[0].Path)
				if err := os.RemoveAll(filepath.Join(m.Repos[1].Source, ".git", "worktrees", filepath.Base(m.Repos[1].Path))); err != nil {
					t.Fatal(err)
				}
			}
			code, out := f.cli("prune")
			if code != 1 || !exists(workspace(f.config(), m.Branch)) || !strings.Contains(out, m.Branch) {
				t.Fatal("pruned a change that would lose work", code, out)
			}
			if kind == "unregistered" && !exists(m.Repos[1].Path) {
				t.Fatal("unregistered worktree directory removed without --force")
			}
			f.mustCLI("prune", "--force")
			if exists(workspace(f.config(), m.Branch)) || exists(m.Repos[1].Path) {
				t.Fatal("forced prune left the change behind")
			}
		})
	}
}

func TestRemoveDiscardsUnregisteredWorktreeOnlyWhenForced(t *testing.T) {
	f := newFixture(t)
	m := f.start()
	if err := os.RemoveAll(filepath.Join(m.Repos[1].Source, ".git", "worktrees", filepath.Base(m.Repos[1].Path))); err != nil {
		t.Fatal(err)
	}
	if code, out := f.cli("remove", m.Branch); code != 1 || !strings.Contains(out, "no longer registered") || !exists(m.Repos[1].Path) {
		t.Fatal(code, out)
	}
	f.mustCLI("remove", m.Branch, "--force")
	if exists(m.Repos[1].Path) || exists(workspace(f.config(), m.Branch)) {
		t.Fatal("forced removal left the unregistered worktree")
	}
}

func TestPruneWithNothingToPrune(t *testing.T) {
	f := newFixture(t)
	f.start()
	out := f.mustCLI("prune")
	if !exists(workspace(f.config(), "feat/shared")) || !strings.Contains(out, "nothing to prune") {
		t.Fatal(out)
	}
}
