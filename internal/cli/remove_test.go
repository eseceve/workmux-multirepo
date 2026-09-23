package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
