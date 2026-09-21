"""Opt-in integration smoke test with real workmux/tmux and inert agent processes.

Run: python tests/smoke_workmux.py
Requires workmux with headless JSON support and tmux. Uses an isolated tmux server,
local Git remotes, XDG directories, and a fake claude executable. No LLM calls.
"""
import contextlib
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
from workmux_multirepo.cli import main
from workmux_multirepo.config import Config
from workmux_multirepo.workspace import load
from workmux_multirepo.terminals import windows


def call(*args):
    return subprocess.check_output(args, stderr=subprocess.STDOUT, text=True).strip()


def cli(*args):
    output = io.StringIO()
    with contextlib.redirect_stdout(output):
        code = main(list(args))
    assert code == 0, output.getvalue()
    print(output.getvalue(), end="")


def smoke():
    tmux = shutil.which("tmux")
    assert tmux and shutil.which("workmux"), "Install tmux and workmux first"
    original_env = dict(os.environ)
    original_cwd = Path.cwd()
    socket = f"wmm-smoke-{os.getpid()}"
    with tempfile.TemporaryDirectory(prefix="wmm-real-") as tmp:
        root = Path(tmp).resolve()
        bin_dir = root / "bin"
        bin_dir.mkdir()
        (bin_dir / "tmux").write_text(f'#!/bin/sh\nexec {shlex.quote(tmux)} -L {shlex.quote(socket)} "$@"\n')
        (bin_dir / "claude").write_text('#!/bin/sh\nexec sleep 120\n')
        for script in bin_dir.iterdir():
            script.chmod(0o755)
        (root / "config" / "workmux").mkdir(parents=True)
        (root / "config" / "workmux" / "config.yaml").write_text("nerdfont: false\n")
        os.environ.update(PATH=f"{bin_dir}:{os.environ['PATH']}", XDG_CONFIG_HOME=str(root / "config"),
                          XDG_STATE_HOME=str(root / "state"), XDG_CACHE_HOME=str(root / "cache"))
        os.environ.pop("TMUX", None)
        os.environ.pop("TMUX_PANE", None)
        os.chdir(root)
        try:
            for name, branch in [("api", "release"), ("web", "develop")]:
                repo = root / name
                call("git", "init", "--bare", "-b", branch, str(root / f"{name}.git"))
                call("git", "init", "-b", branch, str(repo))
                call("git", "-C", str(repo), "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init")
                call("git", "-C", str(repo), "remote", "add", "origin", str(root / f"{name}.git"))
                call("git", "-C", str(repo), "push", "origin", branch)
            cli("init")
            cli("start", "feat/smoke", "api", "web", "--prompt", "Integration smoke only")
            config = Config(root / "wmm.toml")
            manifest = load(config, "feat/smoke")
            session = manifest["session"]
            time.sleep(0.5)
            assert set(windows(session)) == {"build"}, windows(session)
            # Keep a temporary shell so server shutdown doesn't race the review phase.
            call("tmux", "new-window", "-d", "-t", f"{session}:", "-n", "control")
            call("tmux", "kill-window", "-t", windows(session)["build"])
            cli("review", "feat/smoke", "--prepare-pr")
            time.sleep(0.5)
            assert {"wmm-review-api", "wmm-review-web"} <= set(windows(session)), windows(session)
            cli("review", "feat/smoke")
            assert len(windows(session)) == 3, windows(session)
            cli("status", "feat/smoke", "--fields", "repo,state,commits,path")
            print("PASS: real headless worktrees, shared builder, and two independent review windows")
        finally:
            subprocess.run([tmux, "-L", socket, "kill-server"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            os.chdir(original_cwd)
            os.environ.clear()
            os.environ.update(original_env)


if __name__ == "__main__":
    smoke()
