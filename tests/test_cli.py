import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from workmux_multirepo.cli import main
from workmux_multirepo.config import Config
from workmux_multirepo.runtime import Error
from workmux_multirepo.workspace import change_id, directory, load


class WorkflowTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="wmm tests ")
        self.root = Path(self.tmp.name).resolve()
        self.addCleanup(self.tmp.cleanup)
        self.old_cwd = Path.cwd()
        os.chdir(self.root)
        self.addCleanup(os.chdir, self.old_cwd)
        self.calls = []
        self.fail_alias = None
        self.tmux_windows = {}
        self.next_window = 0
        self.repos = {}
        for alias, base in [("api", "release"), ("web", "develop")]:
            repo = self.root / f"{alias} repo"
            remote = self.root / f"{alias}.git"
            self.git("init", "--bare", "-b", base, str(remote))
            self.git("init", "-b", base, str(repo))
            self.git("-C", str(repo), "config", "user.name", "Test")
            self.git("-C", str(repo), "config", "user.email", "test@example.com")
            (repo / "file.txt").write_text("initial\n")
            self.git("-C", str(repo), "add", ".")
            self.git("-C", str(repo), "commit", "-m", "init")
            self.git("-C", str(repo), "remote", "add", "origin", str(remote))
            self.git("-C", str(repo), "push", "-u", "origin", base)
            self.git("-C", str(repo), "remote", "set-head", "origin", "-a")
            self.repos[alias] = repo
        self.config_path = self.root / "wmm.toml"
        self.config_path.write_text('[repos]\napi = "./api repo"\nweb = "./web repo"\n')
        self.config = Config(self.config_path)
        from workmux_multirepo.runtime import run
        self.real_run = run
        self.addCleanup(patch.stopall)
        for module in ["workspace", "terminals"]:
            patch(f"workmux_multirepo.{module}.run", side_effect=self.fake_run).start()
        patch("workmux_multirepo.terminals.shutil.which", side_effect=lambda x: f"/bin/{x}").start()

    def git(self, *args):
        return subprocess.check_output(["git", *args], stderr=subprocess.DEVNULL, text=True).strip()

    def result(self, stdout="", code=0):
        return subprocess.CompletedProcess([], code, stdout, "")

    def fake_run(self, args, cwd=None, check=True):
        args = [str(x) for x in args]
        self.calls.append((args, str(cwd)))
        if args[:3] == ["workmux", "add", "--help"]:
            return self.result("--headless --json")
        if args[:2] == ["workmux", "add"]:
            alias = "api" if Path(cwd) == self.repos["api"] else "web"
            if alias == self.fail_alias:
                raise Error("Simulated setup failure")
            branch = args[2]
            handle = args[args.index("--name") + 1]
            base = args[args.index("--base") + 1]
            path = self.root / "worktrees" / alias / handle
            path.parent.mkdir(parents=True, exist_ok=True)
            self.git("-C", str(cwd), "worktree", "add", "-b", branch, str(path), base)
            return self.result(json.dumps(dict(schema_version=1, branch=branch, worktree_path=str(path))))
        if args[:2] == ["workmux", "open"]:
            name = "wmm-" + args[args.index("--target-name") + 1]
            self.next_window += 1
            self.tmux_windows[name] = f"@{self.next_window}"
            return self.result()
        if args[:2] == ["tmux", "list-windows"]:
            return self.result("\n".join(f"{v}\t{k}" for k, v in self.tmux_windows.items()), 0 if self.tmux_windows else 1)
        if args[:2] in (["tmux", "new-session"], ["tmux", "new-window"]):
            name = args[args.index("-n") + 1]
            self.next_window += 1
            self.tmux_windows[name] = f"@{self.next_window}"
            return self.result(f"@{self.next_window}")
        if args[:2] == ["tmux", "kill-window"]:
            self.tmux_windows = {k: v for k, v in self.tmux_windows.items() if v != args[-1]}
            return self.result()
        if args[0] == "tmux":
            return self.result()
        return self.real_run(args, cwd, check)

    def cli(self, *args):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            code = main(list(args))
        return code, out.getvalue()

    def start(self, *extra):
        result = self.cli("start", "feat/shared", "api", "web", "--no-open", *extra)
        self.assertEqual(result[0], 0, result[1])
        return load(self.config, "feat/shared")

    def test_remote_bases_are_pinned_and_sources_untouched(self):
        self.git("-C", str(self.repos["api"]), "switch", "-c", "other-local-feature")
        manifest = self.start()
        self.assertEqual([r["base"] for r in manifest["repos"]], ["origin/release", "origin/develop"])
        for r in manifest["repos"]:
            self.assertEqual(self.git("-C", r["path"], "rev-parse", "HEAD"), r["base_commit"])
            self.assertEqual(self.git("-C", r["path"], "branch", "--show-current"), "feat/shared")
            self.assertTrue((directory(self.config, "feat/shared") / r["alias"]).is_symlink())
        self.assertEqual(self.git("-C", str(self.repos["api"]), "branch", "--show-current"), "other-local-feature")

    def test_repeated_start_does_not_duplicate_or_refresh_base(self):
        first = self.start()
        second = self.start()
        self.assertEqual(first, second)
        self.assertEqual(sum(args[:2] == ["workmux", "add"] and "--headless" in args for args, _ in self.calls), 2)

    def test_dry_run_has_no_state_or_mutations(self):
        self.assertEqual(self.cli("start", "feat/shared", "api", "web", "--dry-run")[0], 0)
        self.assertFalse(self.config.state.exists())
        self.assertEqual(self.calls, [])
        for repo in self.repos.values():
            self.assertNotIn("feat/shared", self.git("-C", str(repo), "branch"))

    def test_unknown_flags_fail_before_dependencies(self):
        code, out = self.cli("start", "feat/shared", "api", "--typo")
        self.assertEqual(code, 2)
        self.assertIn("--dry-run", out)
        self.assertEqual(self.calls, [])
        self.assertFalse(self.config.state.exists())

    def test_preflight_rejects_existing_branch_before_any_provisioning(self):
        self.git("-C", str(self.repos["web"]), "branch", "feat/shared")
        self.assertEqual(self.cli("start", "feat/shared", "api", "web", "--no-open")[0], 1)
        self.assertEqual(self.calls, [])

    def test_retry_after_partial_failure_preserves_first_worktree(self):
        self.fail_alias = "web"
        self.assertEqual(self.cli("start", "feat/shared", "api", "web", "--no-open")[0], 1)
        partial = load(self.config, "feat/shared")
        self.assertTrue(partial["repos"][0]["ready"])
        self.assertFalse(partial["repos"][1]["ready"])
        marker = Path(partial["repos"][0]["path"]) / "keep.txt"
        marker.write_text("my work")
        self.fail_alias = None
        self.start()
        self.assertEqual(marker.read_text(), "my work")

    def test_status_tracks_uncommitted_and_committed_changes(self):
        manifest = self.start()
        path = Path(manifest["repos"][0]["path"])
        (path / "file.txt").write_text("changed")
        self.git("-C", str(path), "commit", "-am", "change")
        (path / "new.txt").write_text("untracked")
        code, out = self.cli("status", "feat/shared", "--fields", "repo,state,commits,path")
        self.assertEqual(code, 0)
        self.assertIn('"api","modified",1,', out)

    def test_review_refuses_live_builder_and_reuses_existing_windows(self):
        self.start()
        self.tmux_windows["build"] = "@1"
        self.assertEqual(self.cli("review", "feat/shared")[0], 1)
        self.assertFalse(any(a[:2] == ["workmux", "open"] for a, _ in self.calls))
        self.tmux_windows.clear()
        self.assertEqual(self.cli("review", "feat/shared", "--prepare-pr")[0], 0)
        self.assertEqual(self.cli("review", "feat/shared", "--prepare-pr")[0], 0)
        opens = [a for a, _ in self.calls if a[:2] == ["workmux", "open"]]
        self.assertEqual(len(opens), 2)
        self.assertEqual(set(self.tmux_windows), {"wmm-review-api", "wmm-review-web"})
        prompt = (directory(self.config, "feat/shared") / "review-api-prompt.md").read_text()
        self.assertIn("Do not push or publish", prompt)
        self.assertIn("staged, unstaged, and untracked", prompt)

    def test_builder_refuses_active_reviewers(self):
        self.start()
        self.tmux_windows["wmm-review-api"] = "@1"
        self.assertEqual(self.cli("start", "feat/shared", "api", "web")[0], 1)

    def test_builder_launch_is_one_window_and_shell_quotes_prompt(self):
        code, out = self.cli("start", "feat/shared", "api", "web", "--prompt", "Fix $(touch SHOULD_NOT_EXIST)")
        self.assertEqual(code, 0, out)
        creates = [a for a, _ in self.calls if a[:2] == ["tmux", "new-session"]]
        self.assertEqual(len(creates), 1)
        self.assertEqual(set(self.tmux_windows), {"build"})
        self.assertFalse((self.root / "SHOULD_NOT_EXIST").exists())
        self.assertIn("--add-dir", creates[0][-1])

    def test_switched_branch_not_silently_reused(self):
        manifest = self.start()
        self.git("-C", manifest["repos"][0]["path"], "switch", "-c", "oops")
        self.assertEqual(self.cli("review", "feat/shared")[0], 1)
        self.assertEqual(self.cli("start", "feat/shared", "api", "web", "--no-open")[0], 1)

    def test_reject_duplicate_repo_and_traversal(self):
        self.assertEqual(self.cli("start", "feat/shared", "api", "api", "--dry-run")[0], 2)
        self.assertEqual(self.cli("start", "../../oops", "api", "--dry-run")[0], 2)
        self.assertNotEqual(change_id("feat/a-b"), change_id("feat-a/b"))

    def test_configuration_init_is_idempotent(self):
        path = self.root / "other.toml"
        self.assertEqual(self.cli("init", "--config", str(path))[0], 0)
        initial = path.read_text()
        self.assertEqual(self.cli("init", "--config", str(path))[0], 0)
        self.assertEqual(path.read_text(), initial)

    def test_status_empty_and_field_validation(self):
        self.assertIn("changes[0]", self.cli("status")[1])
        self.assertEqual(self.cli("status", "--fields", "secret")[0], 2)


if __name__ == "__main__":
    unittest.main()
