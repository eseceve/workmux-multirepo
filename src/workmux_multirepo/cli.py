"""Public command interface."""
import argparse
import json
import re
import sys
from pathlib import Path
from .config import discover
from .runtime import Error, emit
from .terminals import open_builder, open_reviews, prerequisites, session_name
from .workspace import (brief, directory, fetch_bases, load, locked, plan, provision,
                        save, status_rows)


class Parser(argparse.ArgumentParser):
    def parse_args(self, args=None, namespace=None):
        parsed, unknown = self.parse_known_args(args, namespace)
        if unknown:
            command = getattr(parsed, "command", None)
            target = self
            if command and self._subparsers:
                target = self._subparsers._group_actions[0].choices[command]
            target.error("unrecognized arguments: " + " ".join(unknown))
        return parsed

    def error(self, message):
        raise Error(message, self.format_help().strip(), 2)


def parser():
    p = Parser(prog="wmm", description="Coordinate implementation and review across workmux repositories.", allow_abbrev=False)
    sub = p.add_subparsers(dest="command", parser_class=Parser)
    def command(name, description, example):
        child = sub.add_parser(name, help=description, description=description,
                               epilog=example, allow_abbrev=False)
        child.add_argument("--config", metavar="PATH", help="wmm.toml path (default: search current directory and parents)")
        return child
    init = command("init", "Create workspace configuration from child repositories.", "Example: wmm init")
    init.add_argument("--dry-run", action="store_true", help="show discovered repositories without writing")
    start = command("start", "Create or reopen a shared implementation workspace.", "Examples: wmm start feat/change api web --prompt 'Add checkout' | wmm start feat/change api web --dry-run")
    start.add_argument("branch")
    start.add_argument("repos", nargs="+")
    start.add_argument("--prompt", help="shared change objective (default: ask in the agent session)")
    start.add_argument("--dry-run", action="store_true", help="validate and show the plan without fetching, creating, or launching")
    start.add_argument("--no-open", action="store_true", help="provision worktrees and context without opening an agent")
    review = command("review", "Open an independent reviewer per repository on existing worktrees.", "Examples: wmm review feat/change | wmm review feat/change --prepare-pr --dry-run")
    review.add_argument("branch")
    review.add_argument("--prepare-pr", action="store_true", help="ask reviewers to draft PR text locally; never publish automatically")
    review.add_argument("--dry-run", action="store_true", help="show review targets without opening agents")
    status = command("status", "Show changes or repository state for one change.", "Examples: wmm status | wmm status feat/change --fields repo,state,commits,path")
    status.add_argument("branch", nargs="?")
    status.add_argument("--fields", default="repo,state,commits", help="detail columns: repo,state,commits,base,path,base_commit (default: repo,state,commits)")
    return p


def initialize(args):
    path = Path(args.config or "wmm.toml").resolve()
    if path.exists():
        emit({"config": str(path), "state": "already exists"})
        return
    if not path.parent.is_dir():
        raise Error("Configuration directory does not exist.", "Create the workspace directory before running wmm init.")
    repos = {}
    for repo in sorted(path.parent.iterdir()):
        if repo.is_dir() and (repo / ".git").exists() and not repo.name.startswith("."):
            alias = re.sub(r"[^A-Za-z0-9_-]", "-", repo.name).lstrip("-_")
            if not alias or alias in repos:
                raise Error("Child directory names produce duplicate or empty aliases.", "Create wmm.toml manually with unique aliases.")
            repos[alias] = f"./{repo.name}"
    if not repos:
        raise Error("No child repositories found.", "Run wmm init from the directory containing your Git repositories.")
    if not args.dry_run:
        lines = ["# Paths are relative to this file. Rename keys to choose shorter aliases.",
                 '# build_command = ["claude", "--add-dir", "{repo_paths}", "--", "{prompt}"]',
                 '# review_agent = "claude"', "", "[repos]"]
        lines.extend(f"{json.dumps(k)} = {json.dumps(v)}" for k, v in repos.items())
        with path.open("x") as file:
            file.write("\n".join(lines) + "\n")
    emit({"config": str(path), "repos": [{"repo": k, "path": v} for k, v in repos.items()],
          "help": "Edit aliases in wmm.toml, then run wmm start <branch> <repos...>."})


def start(config, args):
    workspace = directory(config, args.branch)
    existing = (workspace / "manifest.json").exists()
    if existing:
        manifest = load(config, args.branch)
        if args.repos != [r["alias"] for r in manifest["repos"]]:
            raise Error("This change already has a different repository selection.", "Use the original aliases in the same order, or choose a new branch.")
        if any(str(config.repos.get(r["alias"], "")) != r["source"] for r in manifest["repos"]):
            raise Error("Repository paths changed since this workspace was created.", "Restore the original paths in wmm.toml or use another change name.")
        if args.prompt is not None and args.prompt != manifest["objective"]:
            raise Error("This change already has a different objective.", "Edit its BRIEF.md to update the handoff; omit --prompt when reopening.")
        records = manifest["repos"]
    else:
        records = plan(config, args.branch, args.repos)
    if args.dry_run:
        emit({"branch": args.branch, "workspace": str(workspace), "fetch": "on execution (dry-run uses local refs)",
              "repos": [{"repo": r["alias"], "base": r["base"], "handle": r["handle"]} for r in records]})
        return
    prerequisites(config, builder=not args.no_open)
    if not existing:
        fetch_bases(records)
        if workspace.exists():
            raise Error(f"Workspace path exists without a manifest: {workspace}", "Move that directory aside after inspecting its contents, then retry.")
        workspace.mkdir()
        manifest = dict(version=1, branch=args.branch, phase="provisioning", repos=records,
                        objective=args.prompt or "Ask the user to describe the intended change before editing.",
                        session=session_name(config, args.branch))
        save(config, manifest)
    # Reopening a ready workspace must not erase its lifecycle phase.
    if not all(r["ready"] for r in manifest["repos"]):
        provision(config, manifest)
    else:
        from .workspace import verify
        for record in manifest["repos"]:
            verify(record, args.branch)
    brief(config, manifest)
    if not args.no_open:
        open_builder(config, manifest)
    emit({"branch": args.branch, "phase": manifest["phase"], "workspace": str(workspace),
          "help": f"tmux attach -t {manifest['session']}" if not args.no_open else f"wmm review {args.branch}"})


def status(config, args):
    allowed = {"repo", "state", "commits", "base", "path", "base_commit"}
    fields = args.fields.split(",")
    if not fields or len(set(fields)) != len(fields) or set(fields) - allowed:
        raise Error("Invalid --fields selection.", "Valid fields: repo,state,commits,base,path,base_commit", 2)
    if args.branch:
        manifest = load(config, args.branch)
        emit({"branch": args.branch, "phase": manifest["phase"], "session": manifest["session"],
              "repos": status_rows(manifest, fields)})
    else:
        rows = []
        for path in sorted(config.state.glob("*/manifest.json")):
            data = json.loads(path.read_text())
            rows.append(dict(branch=data["branch"], phase=data["phase"], repos=len(data["repos"])))
        emit({"changes": rows, "help": "wmm start <branch> <repos...>" if not rows else "wmm status <branch>"})


def main(argv=None):
    p = parser()
    try:
        args = p.parse_args(argv)
        if args.command == "init":
            initialize(args)
            return 0
        if args.command is None:
            emit({"bin": str(Path(sys.argv[0]).resolve()),
                  "description": "Coordinate one implementation agent and per-repository reviews."})
            args = p.parse_args(["status"])
        config = discover(args.config)
        if args.command == "status":
            status(config, args)
        elif args.command == "start":
            if args.dry_run:
                start(config, args)
            else:
                with locked(config):
                    start(config, args)
        else:
            manifest = load(config, args.branch)
            if args.dry_run:
                emit({"branch": args.branch, "repos": [{"repo": r["alias"], "path": r["path"] or "incomplete"} for r in manifest["repos"]], "prepare_pr": args.prepare_pr})
            else:
                with locked(config):
                    manifest = load(config, args.branch)
                    prerequisites(config)
                    open_reviews(config, manifest, args.prepare_pr)
                emit({"branch": args.branch, "phase": manifest["phase"], "help": f"tmux attach -t {manifest['session']}"})
        return 0
    except Error as exc:
        emit({"error": str(exc), "help": exc.hint})
        return exc.code
    except (OSError, ValueError, KeyError, TypeError) as exc:
        emit({"error": f"Invalid configuration or workspace data: {exc}", "help": "Inspect wmm.toml and the workspace manifest; existing worktrees were preserved."})
        return 1
    except KeyboardInterrupt:
        emit({"error": "Interrupted; existing worktrees were preserved.", "help": "Retry the same command to continue."})
        return 1


if __name__ == "__main__":
    sys.exit(main())
