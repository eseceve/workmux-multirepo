"""Process, persistence and output boundaries."""
import json
import os
import subprocess
from pathlib import Path


class Error(Exception):
    def __init__(self, message, hint="Run wmm --help for available commands.", code=1):
        super().__init__(message)
        self.hint = hint
        self.code = code


def run(args, cwd=None, check=True):
    env = dict(os.environ, GIT_TERMINAL_PROMPT="0", GCM_INTERACTIVE="never", WORKMUX_BACKEND="tmux")
    env.setdefault("GIT_SSH_COMMAND", "ssh -oBatchMode=yes")
    # Agents run interactively in their own terminal; provisioning never reads stdin.
    try:
        result = subprocess.run(
            [str(a) for a in args], cwd=cwd, env=env, stdin=subprocess.DEVNULL,
            text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
    except FileNotFoundError:
        raise Error(f"Required executable unavailable: {args[0]}",
                    "Install the prerequisites listed in the README and retry.") from None
    if check and result.returncode:
        # Keep potentially sensitive subprocess output off structured stdout.
        import sys
        print(result.stderr.strip(), file=sys.stderr)
        raise Error(f"Operation failed in {cwd or Path.cwd()}: {args[0]} {args[1]}",
                    "Inspect stderr, fix the failure, and retry the same wmm command; existing work is preserved.")
    return result


def git(repo, *args, check=True):
    return run(["git", "-C", str(repo), *args], check=check)


def atomic_json(path, value):
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(value, indent=2) + "\n")
    tmp.replace(path)


def emit(data):
    """TOON subset: scalar fields and arrays of flat uniform objects.

    Quote every string using JSON escapes, as allowed by TOON 3.0.
    """
    def scalar(value):
        return json.dumps(value, ensure_ascii=False)

    for key, value in data.items():
        if isinstance(value, list):
            if not value:
                print(f"{key}[0]:")
            elif isinstance(value[0], dict):
                fields = list(value[0])
                print(f"{key}[{len(value)}]{{{','.join(fields)}}}:")
                for row in value:
                    print("  " + ",".join(scalar(row[f]) for f in fields))
            else:
                print(f"{key}[{len(value)}]: " + ",".join(map(scalar, value)))
        else:
            print(f"{key}: {scalar(value)}")
