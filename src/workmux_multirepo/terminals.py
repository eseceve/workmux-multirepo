"""One tmux session per change; workmux owns the per-repository review windows."""
import hashlib
import json
import os
import shlex
import shutil
from .runtime import Error, run
from .workspace import brief, directory, save, verify


def session_name(config, branch):
    token = hashlib.sha256(f"{config.root}:{branch}".encode()).hexdigest()[:12]
    return f"wmm-{token}"


def windows(session):
    result = run(["tmux", "list-windows", "-t", f"={session}", "-F", "#{window_id}\t#{window_name}"], check=False)
    if result.returncode:
        return {}
    return {line.split("\t", 1)[1]: line.split("\t", 1)[0] for line in result.stdout.splitlines()}


def prerequisites(config, builder=False):
    for name in ["workmux", "tmux"] + ([config.build_command[0]] if builder else []):
        if not shutil.which(name):
            raise Error(f"Executable unavailable: {name}", "Install the README prerequisites or correct build_command in wmm.toml.")
    help_text = run(["workmux", "add", "--help"]).stdout
    if "--headless" not in help_text or "--json" not in help_text:
        raise Error("Installed workmux does not support headless provisioning.", "Update workmux to a release supporting add --headless --json, then retry.")


def open_builder(config, manifest):
    session = manifest["session"]
    active = windows(session)
    if any(name.startswith("wmm-review-") for name in active):
        raise Error("Review windows are still open.", "Exit the review agents before reopening the shared builder.")
    if "build" in active:
        return
    workspace = directory(config, manifest["branch"])
    context = brief(config, manifest)
    prompt = (f"Implement the coordinated change described in {context}. "
              "Read the root workspace instructions and each repository's AGENTS.md / CLAUDE.md before editing. "
              f"Original workspace: {config.root}. Only edit the worktrees listed in manifest.json, never their source checkouts. "
              "Ask for the objective if it is not specified. Coordinate contracts across repos and run relevant checks. "
              "Update BRIEF.md with the integration contract, verification and a review handoff. "
              "Do not push, publish PRs, or merge unless the user explicitly requests it. "
              "Exit this agent when implementation is ready so wmm review can launch independent reviewers.")
    (workspace / "build-prompt.md").write_text(prompt + "\n")
    command = []
    for arg in config.build_command:
        if arg == "{repo_paths}":
            command.extend(r["path"] for r in manifest["repos"])
        else:
            command.append({"{prompt}": prompt, "{workspace}": str(workspace)}.get(arg, arg))
    command[0] = shutil.which(command[0]) or command[0]
    shell_command = "exec " + shlex.join(command)
    if active:
        created = run(["tmux", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", f"{session}:", "-n", "build", "-c", workspace, shell_command])
    else:
        created = run(["tmux", "new-session", "-d", "-P", "-F", "#{window_id}", "-s", session, "-n", "build", "-c", workspace, shell_command])
    # Agent commands can trigger automatic renaming in the user's tmux config.
    # Set a stable name so phase-transition checks do not depend on process titles.
    if created.returncode == 0 and created.stdout.strip():
        window = created.stdout.strip()
        run(["tmux", "set-window-option", "-t", window, "automatic-rename", "off"], check=False)
        run(["tmux", "rename-window", "-t", window, "build"], check=False)
    manifest["phase"] = "implementation"
    save(config, manifest)
    if os.environ.get("TMUX"):
        run(["tmux", "switch-client", "-t", session], check=False)


def open_reviews(config, manifest, prepare_pr=False):
    session = manifest["session"]
    active = windows(session)
    if "build" in active:
        raise Error("The shared builder window is still open.", "Exit the builder agent/window, then rerun wmm review. No agents were stopped.")
    for record in manifest["repos"]:
        if not record["ready"]:
            raise Error("Provisioning is incomplete.", "Rerun wmm start with the original branch and repositories first.")
        verify(record, manifest["branch"])
    workspace = directory(config, manifest["branch"])
    context = brief(config, manifest)
    review_config = workspace / "review.workmux.yaml"
    review_config.write_text(
        f"agent: {json.dumps(config.review_agent)}\nwindow_prefix: wmm-\n"
        "mode: window\npanes:\n  - command: <agent>\n    focus: true\n  - split: horizontal\n    size: 12\n"
    )
    placeholder = None
    if not active:
        placeholder = run(["tmux", "new-session", "-d", "-P", "-F", "#{window_id}",
                           "-s", session, "-n", "starting", "-c", workspace]).stdout.strip()
    try:
        for record in manifest["repos"]:
            target = f"review-{record['alias']}"
            if f"wmm-{target}" in windows(session):
                continue
            prompt = (f"Review only {record['alias']} in {record['path']}. "
                      f"Read {context} and the repository's AGENTS.md / CLAUDE.md. "
                      f"Compare against original base commit {record['base_commit']} ({record['base']}); "
                      "include staged, unstaged, and untracked changes, not just commits. "
                      "Inspect sibling repositories for contract compatibility but do not edit them. "
                      "Find correctness defects, fix this repository's issues, and run its relevant checks. "
                      f"Write findings and validation to {workspace / ('review-' + record['alias'] + '.md')}. "
                      "Do not merge. ")
            if prepare_pr:
                prompt += ("Prepare a local PR title and description including cross-repo dependencies and validation. "
                           "Do not push or publish the PR; wait for explicit user authorization.")
            else:
                prompt += "Do not push or publish PRs."
            prompt_path = workspace / f"review-{record['alias']}-prompt.md"
            prompt_path.write_text(prompt + "\n")
            run(["workmux", "open", record["handle"], "--mode", "window", "--parent-session", session,
                 "--target-name", target, "--config", review_config, "--prompt-file", prompt_path], cwd=record["source"])
            manifest["phase"] = "review"
            save(config, manifest)
    finally:
        # Only close the empty launcher after at least one reviewer window exists.
        if placeholder and any(n.startswith("wmm-review-") for n in windows(session)):
            run(["tmux", "kill-window", "-t", placeholder], check=False)
    if os.environ.get("TMUX"):
        run(["tmux", "switch-client", "-t", session], check=False)
