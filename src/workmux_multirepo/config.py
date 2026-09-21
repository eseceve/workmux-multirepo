"""Workspace configuration with paths relative to the config file."""
import re
import tomllib
from pathlib import Path
from .runtime import Error

DEFAULT_BUILD = ["claude", "--add-dir", "{repo_paths}", "--", "{prompt}"]


class Config:
    def __init__(self, path):
        self.path = path.resolve()
        self.root = self.path.parent
        try:
            data = tomllib.loads(self.path.read_text())
        except (OSError, tomllib.TOMLDecodeError) as exc:
            raise Error(f"Cannot read configuration: {exc}", "Run wmm init, then edit wmm.toml.") from None
        unknown = set(data) - {"repos", "build_command", "review_agent"}
        if unknown:
            raise Error(f"Unknown configuration keys: {', '.join(sorted(unknown))}", "Use repos, build_command, or review_agent.", 2)
        repos = data.get("repos", {})
        if not isinstance(repos, dict) or not repos:
            raise Error("No repositories configured.", "Add aliases and paths under [repos] in wmm.toml.", 2)
        self.repos = {}
        for name, path in repos.items():
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", name) or not isinstance(path, str):
                raise Error("Repository aliases must be simple names with string paths.", "Use api = './my-api' under [repos].", 2)
            self.repos[name] = (self.root / Path(path).expanduser()).resolve()
        self.build_command = data.get("build_command", DEFAULT_BUILD)
        if not isinstance(self.build_command, list) or not self.build_command or not all(isinstance(x, str) and x for x in self.build_command):
            raise Error("build_command must be a nonempty array of arguments.", code=2)
        allowed = {"{repo_paths}", "{prompt}", "{workspace}"}
        for arg in self.build_command:
            if ("{" in arg or "}" in arg) and arg not in allowed:
                raise Error(f"Unknown command placeholder: {arg}", "Use whole arguments {repo_paths}, {prompt}, or {workspace}.", 2)
        self.review_agent = data.get("review_agent", "claude")
        if not isinstance(self.review_agent, str) or not self.review_agent.strip():
            raise Error("review_agent must be an agent command string.", code=2)
        self.state = self.root / ".wmm"


def discover(explicit=None):
    if explicit:
        return Config(Path(explicit))
    for parent in [Path.cwd(), *Path.cwd().parents]:
        if (parent / "wmm.toml").is_file():
            return Config(parent / "wmm.toml")
    raise Error("No wmm.toml found.", "Run wmm init in the directory containing your repositories.")
