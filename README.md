# workmux-multirepo

Implement a change across repositories with one agent, then review each repository independently. Built on [workmux](https://github.com/raine/workmux), Git, and tmux. Independent project; not an official workmux plugin.

Both `wmm` and `workmux-multirepo` are installed as executable commands. No shell alias or function is required.

## Install

Written in Go. Requires Git, tmux, and a workmux release with `add --headless --json` at runtime. The default interactive agent is Claude Code; the builder command and reviewer agent are configurable. Supported platforms: macOS and Linux.

```sh
gh repo clone eseceve/workmux-multirepo
cd workmux-multirepo
go install ./cmd/wmm ./cmd/workmux-multirepo
```

Building requires Go 1.24+. The repository is currently private, so cloning requires GitHub access. Ensure Go's binary directory is on your PATH, or choose an existing PATH directory:

```sh
GOBIN="$HOME/.local/bin" go install ./cmd/wmm ./cmd/workmux-multirepo
```

## Configure a workspace

Run this in the directory containing your repositories:

```sh
wmm init
```

Edit the generated `wmm.toml` to choose short aliases:

```toml
# Optional; these are the defaults.
build_command = ["claude", "--add-dir", "{repo_paths}", "--", "{prompt}"]
review_agent = "claude"

[repos]
oo = "./order-orchestrator"
bff = "./marketplace-bff"
web = "./ai-funnel-webapp"
```

Paths are relative to the configuration file. Every command accepts `--config /path/to/wmm.toml`; otherwise discovery searches the current directory and its parents. Add `.wmm/` to your workspace's ignore file if that directory itself is versioned.

`build_command` is an argument array, not a shell snippet. Whole-argument placeholders are `{workspace}`, `{prompt}`, and `{repo_paths}` (expands to multiple arguments). This lets you use another agent executable and its directory-access flags. `review_agent` is a command understood by workmux, which handles its prompt injection. No model or permission-bypass flags are imposed, and no agent hooks are installed automatically.

## Build once across repositories

```sh
wmm start feat/checkout oo bff web --prompt 'Describe the coordinated change here'
```

`start` validates all repositories, discovers and fetches each origin's remote default branch, then pins its commit before provisioning. It does not use a locally checked-out feature branch. Each worktree uses the same feature branch name, while its base may differ (`origin/release`, `origin/develop`, etc.). Unrelated existing branches are rejected.

Workmux creates and provisions each worktree headlessly using its existing project/global configuration, including file operations and setup hooks. Wmm records the returned paths and creates a shared workspace:

```text
.wmm/feat-checkout-<hash>/
├── manifest.json
├── BRIEF.md
├── build-prompt.md
├── oo  -> <workmux-created-worktree>
├── bff -> <workmux-created-worktree>
└── web -> <workmux-created-worktree>
```

These repository entries are **symlinks**; workmux retains ownership of the real worktree locations. The default builder receives explicit directory access to those locations. Each repository keeps its own Git history and instructions.

One tmux session is created for the change, containing one builder window. From within tmux, wmm switches to that session; outside tmux, it prints an attach command. The builder receives the shared brief and is instructed to read each repo's instructions, coordinate contracts, validate the integration, and leave a handoff in `BRIEF.md`.

```sh
# Preview without network calls, ref updates, files, or windows.
wmm start feat/checkout oo bff web --dry-run

# Provision and write context without launching an agent.
wmm start feat/checkout oo bff web --no-open

# Reopen without duplicating worktrees or moving the original bases.
wmm start feat/checkout oo bff web
```

Without `--prompt`, the builder asks for the objective before editing. For an existing change, update `BRIEF.md` to refine its objective or contracts.

## Review independently

Exit the builder agent/window, then run:

```sh
wmm review feat/checkout
```

Wmm opens one workmux review window per repository, **on the same worktrees**. Reviewers receive the shared brief, their original base commit, and instructions to include staged, unstaged, and untracked changes. Each may fix and test its own repository while inspecting siblings for compatibility. They must not edit sibling repositories or merge.

```sh
# Also ask each reviewer to write a local PR title and description.
wmm review feat/checkout --prepare-pr
```

This drafts PR material; it does not authorize publishing or pushing. Ask the reviewer explicitly when ready to publish. Review summaries are written beside `BRIEF.md`.

`review` refuses to start while the managed builder window is open. `start` refuses to reopen the builder while managed review windows are open. This is a terminal lifecycle guard, not an OS-level file lock against unrelated agents/editors. Keep managed window names intact. Repeating `review` reuses existing windows and does not inject a new prompt into an already-running agent; choose `--prepare-pr` when first opening it.

Review windows use a generated workmux layout with an agent and a shell. Global workmux settings still apply; repository-specific pane layouts are replaced for the review phase. The shared builder is managed directly through tmux and does not appear as a workmux worktree itself.

## Inspect progress

```sh
wmm status
wmm status feat/checkout
wmm status feat/checkout --fields repo,state,commits,base,path
```

Status shows local Git state and commits since each pinned base. It does not claim an agent has finished, tests have passed, or a PR has merged. The phase records the most recent lifecycle transition. Running `wmm` without arguments shows workspace changes and contextual next steps.

Output uses a compact [TOON](https://toonformat.dev/) subset. Progress/diagnostics go to stderr; structured results and errors go to stdout. Exit codes: `0` success, `1` operational failure, `2` invalid input.

## Failure and cleanup behavior

Operations are serialized per workspace. A failure never deletes work already created. If provisioning stops, fix the error and repeat the original command; completed repositories are preserved and skipped. If a setup hook fails **after creating its Git worktree**, wmm refuses to call it ready or silently adopt it: inspect the incomplete worktree and preserve any work before removing it and retrying.

There is no group removal command in this first release. Worktrees remain compatible with ordinary workmux commands run from their real directories. The shared workspace and manifest are retained for inspection. No commits, pushes, merges, or real agent API calls are performed by wmm itself; interactive agents act under their own permission settings.

## Development

```sh
make build   # bin/wmm and bin/workmux-multirepo
make test    # Go tests with the race detector and coverage
make lint    # go vet

# Opt-in integration test: actual workmux/tmux, local remotes, inert fake agents.
make smoke
```

Unit/integration-style tests use real temporary Git repositories with simulated terminal processes. The opt-in smoke test isolates its tmux server and XDG state and never invokes an LLM.

The implementation follows the thin-wrapper approach used by [pier](https://github.com/eseceve/pier): Cobra commands, a replaceable process runner for tests, and GoReleaser configuration for macOS/Linux binaries. No Python or Node runtime is required.

GoReleaser can build local archives with `goreleaser release --snapshot --clean`. Publishing a release remains an explicit maintainer action.
