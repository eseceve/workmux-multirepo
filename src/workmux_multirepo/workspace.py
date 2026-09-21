"""Persistent changes: plan, provision, and inspect repository worktrees."""
import fcntl
import hashlib
import json
import re
from contextlib import contextmanager
from pathlib import Path
from .runtime import Error, atomic_json, git, run


def change_id(branch):
    slug = re.sub(r"[^A-Za-z0-9_-]", "-", branch)[:48]
    return f"{slug}-{hashlib.sha256(branch.encode()).hexdigest()[:10]}"


def directory(config, branch):
    return config.state / change_id(branch)


@contextmanager
def locked(config):
    config.state.mkdir(parents=True, exist_ok=True)
    with (config.state / ".lock").open("w") as file:
        try:
            fcntl.flock(file, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Error("Another wmm mutation is running in this workspace.", "Retry after that command finishes.") from None
        yield


def load(config, branch):
    path = directory(config, branch) / "manifest.json"
    if not path.is_file():
        raise Error(f"Change not found: {branch}", "Run wmm status to list changes, or wmm start <branch> <repos...>.")
    try:
        data = json.loads(path.read_text())
    except (ValueError, OSError):
        raise Error("Cannot read the workspace manifest.", f"Restore {path} from a backup; worktrees have not been changed.") from None
    if data.get("version") != 1 or data.get("branch") != branch:
        raise Error("Unsupported or mismatched workspace manifest.")
    return data


def save(config, manifest):
    atomic_json(directory(config, manifest["branch"]) / "manifest.json", manifest)


def plan(config, branch, aliases):
    if not aliases or len(aliases) != len(set(aliases)):
        raise Error("Choose at least one repository without duplicate aliases.", "Run wmm start <branch> <repo> [repo...].", 2)
    unknown = set(aliases) - config.repos.keys()
    if unknown:
        raise Error(f"Unknown repositories: {', '.join(sorted(unknown))}", f"Configured aliases: {', '.join(config.repos)}", 2)
    if len({config.repos[a] for a in aliases}) != len(aliases):
        raise Error("Two selected aliases point to the same repository.", "Select each repository once.", 2)
    if branch.startswith("-") or git(config.root, "check-ref-format", "--branch", branch, check=False).returncode:
        raise Error(f"Invalid branch: {branch}", "Use a valid Git branch such as feat/my-change.", 2)
    records = []
    commons = set()
    for alias in aliases:
        repo = config.repos[alias]
        result = git(repo, "rev-parse", "--show-toplevel", check=False)
        if result.returncode or Path(result.stdout.strip()).resolve() != repo:
            raise Error(f"Not a repository root: {repo}", "Correct the repository path in wmm.toml.")
        common = git(repo, "rev-parse", "--path-format=absolute", "--git-common-dir").stdout.strip()
        if common in commons:
            raise Error("Selected paths share the same Git repository.", "Select each Git repository once.", 2)
        commons.add(common)
        if git(repo, "show-ref", "--verify", "--quiet", f"refs/heads/{branch}", check=False).returncode == 0:
            raise Error(f"Branch already exists in {alias}: {branch}", "Choose a new branch; wmm never adopts unrelated existing branches.")
        # Dry-run deliberately does not contact the network or change refs.
        base = git(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD", check=False).stdout.strip()
        records.append(dict(alias=alias, source=str(repo), base=base or "origin/<remote-default>",
                            base_commit=None, path=None, handle=f"wmm-{change_id(branch)}-{alias}", ready=False))
    return records


def fetch_bases(records):
    for record in records:
        repo = record["source"]
        remote = git(repo, "ls-remote", "--symref", "origin", "HEAD").stdout
        match = re.search(r"^ref: refs/heads/(.+)\s+HEAD$", remote, re.MULTILINE)
        if not match:
            raise Error(f"Remote default branch unavailable for {record['alias']}.", "Configure origin with a default branch, then retry wmm start.")
        default = match.group(1)
        git(repo, "fetch", "origin", f"+refs/heads/{default}:refs/remotes/origin/{default}")
        git(repo, "symbolic-ref", "refs/remotes/origin/HEAD", f"refs/remotes/origin/{default}")
        record["base"] = f"origin/{default}"
        record["base_commit"] = git(repo, "rev-parse", f"refs/remotes/origin/{default}^{{commit}}").stdout.strip()


def worktree_for(record, branch):
    text = git(record["source"], "worktree", "list", "--porcelain", "-z").stdout
    for entry in text.split("\0\0"):
        fields = dict(part.split(" ", 1) for part in entry.split("\0") if " " in part)
        if fields.get("branch") == f"refs/heads/{branch}":
            return fields.get("worktree")
    return None


def verify(record, branch):
    path = record.get("path")
    actual = worktree_for(record, branch)
    if not path or not actual or Path(actual).resolve() != Path(path).resolve():
        raise Error(f"Worktree missing or branch changed for {record['alias']}.", "Restore the recorded worktree and branch before retrying; see wmm status <branch>.")


def provision(config, manifest):
    workspace = directory(config, manifest["branch"])
    for record in manifest["repos"]:
        if record["ready"]:
            verify(record, manifest["branch"])
        else:
            actual = worktree_for(record, manifest["branch"])
            if actual:
                # A hook may have failed after Git created the worktree. Do not silently
                # adopt it or call it ready: provisioning completion must be explicit.
                raise Error(f"Incomplete provisioning in {record['alias']}: {actual}",
                            "Inspect the failed setup. Remove that incomplete worktree after preserving any work, then retry wmm start.")
            result = run(["workmux", "add", manifest["branch"], "--headless", "--json",
                          "--name", record["handle"], "--base", record["base_commit"]], cwd=record["source"])
            try:
                receipt = json.loads(result.stdout)
                path = Path(receipt["worktree_path"]).resolve()
                if receipt["schema_version"] != 1 or receipt["branch"] != manifest["branch"]:
                    raise ValueError()
            except (ValueError, KeyError, TypeError):
                raise Error("Unsupported provisioning receipt.", "Use a workmux version with headless JSON schema version 1.") from None
            record["path"] = str(path)
            verify(record, manifest["branch"])
            record["ready"] = True
            save(config, manifest)
        link = workspace / record["alias"]
        if link.is_symlink():
            if link.resolve() != Path(record["path"]):
                raise Error(f"Workspace link points elsewhere: {link}")
        elif link.exists():
            raise Error(f"Workspace path already exists: {link}")
        else:
            link.symlink_to(record["path"], target_is_directory=True)
    manifest["phase"] = "ready"
    save(config, manifest)


def brief(config, manifest):
    workspace = directory(config, manifest["branch"])
    path = workspace / "BRIEF.md"
    if not path.exists():
        rows = "\n".join(f"- {r['alias']}: {r['path']} (base {r['base']} at {r['base_commit']})" for r in manifest["repos"])
        path.write_text(f"# {manifest['branch']}\n\n## Objective\n\n{manifest['objective']}\n\n## Repositories\n\n{rows}\n\n## Contracts and implementation handoff\n\nRecord cross-repository contracts, verification, and remaining questions here before review.\n")
    return path


def status_rows(manifest, fields):
    rows = []
    for record in manifest["repos"]:
        path = record.get("path")
        row = dict(repo=record["alias"], state="incomplete", commits=0, base=record["base"],
                   path=path or "", base_commit=record["base_commit"] or "")
        if record["ready"] and path:
            try:
                verify(record, manifest["branch"])
                dirty = git(path, "status", "--porcelain").stdout
                row["state"] = "modified" if dirty else "clean"
                row["commits"] = int(git(path, "rev-list", "--count", f"{record['base_commit']}..HEAD").stdout)
            except Error:
                row["state"] = "missing-or-switched"
        rows.append({key: row[key] for key in fields})
    return rows
