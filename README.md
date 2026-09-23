# workmux-multirepo

Implement a change across repositories with one agent, then review each repository independently. Built on [workmux](https://github.com/raine/workmux), Git, and tmux. Independent project; not an official workmux plugin.

Both `wmm` and `workmux-multirepo` are installed as executable commands. No shell alias or function is required.

## Install

Written in Go. Requires Git, tmux, and workmux. Agents are launched by workmux using its configuration; wmm has no agent default of its own. Supported platforms: macOS and Linux.

Wmm checks workmux compatibility automatically. It uses `add --headless --json` internally to provision worktrees and read their actual paths. You do not pass or configure these flags; an older incompatible workmux release needs an update.

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
[repos]
oo = "./order-orchestrator"
bff = "./marketplace-bff"
web = "./ai-funnel-webapp"
```

Paths are relative to the configuration file. Every command accepts `--config /path/to/wmm.toml`; otherwise discovery searches the current directory and its parents. Add `.wmm/` to your workspace's ignore file if that directory itself is versioned.

`wmm.toml` contains repository mappings only. Configure agents, models, panes, prefixes, hooks, and permissions in workmux:

- The shared builder uses the global workmux configuration, with `.workmux.yaml` (or `.workmux.yml`) beside `wmm.toml` as its project configuration when present.
- Each reviewer uses workmux's normal configuration resolution from its source repository, including global settings and that repository's project overrides.
- Wmm only passes prompts when explicitly requested with `--prompt` or `--prepare-pr`; it does not construct agent commands or parse/merge workmux configuration.

To launch an agent, the pane layout must include an agent pane. Explicit prompts require one. Workmux's default
shell/clear layout does not. For example, add this to
`~/.config/workmux/config.yaml` so builders and reviewers inherit it:

```yaml
panes:
  - command: <agent>
    focus: true
  - split: horizontal
```

`<agent>` uses workmux's configured agent (`claude` by default). Project-specific
`panes:` overrides must also include an agent pane. If start reports
"no pane is configured to run the agent", configure the pane and rerun the same
`wmm start` command; the provisioned worktrees are preserved.

The old `build_command` and `review_agent` keys are no longer accepted. Remove them from `wmm.toml` and put the corresponding settings in workmux. No model or permission-bypass flags are imposed, and no agent hooks are installed automatically.

## Build once across repositories

```sh
wmm start feat/checkout oo bff web
```

`start` validates all repositories and pins each origin's locally recorded default branch before provisioning, without contacting the remote. It does not use a locally checked-out feature branch. Each worktree uses the same feature branch name, while its base may differ (`origin/release`, `origin/develop`, etc.). Unrelated existing branches are rejected.

The base reflects your last fetch and may be behind the remote. Use `wmm start feat/checkout oo bff web --fetch` to discover and fetch the current remote default branches before creating a new workspace. This is also required if a local default-branch reference is missing. Reopening an existing workspace always keeps its original pinned bases, even with `--fetch`. Repository setup hooks still run through workmux and may perform their own network operations.

Workmux creates and provisions each worktree headlessly using its existing project/global configuration, including file operations and setup hooks. Wmm records the returned paths and creates a shared workspace:

```text
.wmm/feat-checkout-<hash>/
├── manifest.json
├── .git/  # internal coordination repository for workmux
├── oo  -> <workmux-created-worktree>
├── bff -> <workmux-created-worktree>
└── web -> <workmux-created-worktree>
```

These repository entries are **symlinks**; workmux retains ownership of the real worktree locations. Each repository keeps its own Git history and instructions. Agent permissions still apply to the real paths: grant access through your agent/workmux configuration as needed, including mounts if using a sandbox. Wmm does not inject agent-specific directory-access flags.

Workmux needs a Git repository to open an agent. On the first builder launch, wmm initializes a local coordination repository here with one empty commit and no remote. Code changes and commits belong in the individual worktrees, not this internal repository.

Inside tmux, wmm opens a builder window in the current session. The builder window is named after the full branch (also when reopening); tmux metadata identifies each change so multiple changes can share a session. Outside tmux, wmm creates a dedicated session and prints an attach command. Existing windows in another session must be closed before reopening the builder in the current session.

No `BRIEF.md` or prompt files are generated by wmm, and no initial prompt is sent by default. Existing files from previous versions are left untouched.
```sh
# Preview without network calls, ref updates, files, or windows.
wmm start feat/checkout oo bff web --dry-run

# Provision worktrees without opening a window.
wmm start feat/checkout oo bff web --no-open

# Reopen without duplicating worktrees or moving the original bases.
wmm start feat/checkout oo bff web
```

Use `--prompt 'Describe the change'` to explicitly send an initial prompt to a newly opened builder. Reopening without that flag does not replay a previous prompt. Workmux handles storage and delivery of explicitly requested prompts.

If `start` fails, fix the reported cause and repeat the same command. Completed repositories are reused with their work preserved. An incomplete worktree is recorded and, if it has no changes, ignored/untracked files, or new commits, removed through workmux with its branch kept and provisioned again so setup hooks run. If it contains work, recovery stops and reports its location instead of deleting it. Hooks may therefore run more than once after a failure; external effects of hooks cannot be rolled back by wmm.
## Review independently

When implementation is ready, run this from a shell pane in the builder window:

```sh
wmm review feat/checkout
```

Wmm adds each repository’s reviewer panes to that same window, **on the same worktrees**. Your existing panes stay open. If invoked from another window, wmm creates one review window in the invoking tmux session and groups all reviewer panes there. Without tmux, it uses the recorded session or creates it. An existing review group in the target session is reused. By default these windows receive no initial prompt; give the agents your review instructions directly. Original base commits remain available through `wmm status feat/checkout --fields repo,base,base_commit,path`.
```sh
# Also ask each reviewer to write a local PR title and description.
wmm review feat/checkout --prepare-pr
```

This drafts PR material; it does not authorize publishing or pushing. Ask the reviewer explicitly when ready to publish. With `--prepare-pr`, an explicit prompt includes the original base commit and asks for review of committed and uncommitted changes and local PR text.

Window and pane metadata identify the change and repository, so renaming a window does not affect detection. Repeating `review` reuses existing reviewer panes and does not resend prompts; choose `--prepare-pr` when first opening them. `start` refuses to reopen the builder while its managed review group remains open. Starting review leaves implementation processes alive; it does not detect whether an agent has finished editing.

Workmux launches each reviewer with its repository’s configured agent and panes. Wmm moves those live panes into the destination window and arranges them with tmux’s tiled layout. Workmux may briefly show launch windows during this transfer; they disappear when their last pane is moved. Both agent and shell panes are preserved. If a transfer fails, rerun `review` to finish moving the remaining panes.

Workmux’s window prefix still applies. A workmux `windows:` layout requires session mode and is incompatible with this workflow; wmm reports workmux’s error rather than replacing that layout.

## Inspect progress

```sh
wmm status
wmm status feat/checkout
wmm status feat/checkout --fields repo,state,commits,base,path
```

Status shows local Git state and commits since each pinned base. It does not claim an agent has finished, tests have passed, or a PR has merged. The phase records the most recent lifecycle transition. Running `wmm` without arguments shows workspace changes and contextual next steps.

Output uses a compact [TOON](https://toonformat.dev/) subset. Progress/diagnostics go to stderr; structured results and errors go to stdout. Exit codes: `0` success, `1` operational failure, `2` invalid input.

## Remove a change

```sh
# Remove all of the change's worktrees, local branches, and managed windows.
wmm remove feat/checkout

# Keep the local branches and their commits.
wmm remove feat/checkout --keep-branch

# Preview, or explicitly discard unsaved files and unmerged commits.
wmm remove feat/checkout --dry-run
wmm remove feat/checkout --force
```

Removal works before or after review, including after an incomplete `start`. It checks every repository before making changes. By default it refuses tracked, untracked, or ignored files that would be lost, extra workspace files, and branch commits not merged into the locally recorded base. `--keep-branch` preserves commits but still protects unsaved files; combine it with `--force` to discard files while retaining branches. Remote branches are never deleted. Only windows tagged for this workspace are closed; unrelated windows remain open.

If removal fails partway through, repeat the same command to finish. Its manifest records progress until cleanup completes; `start` and `review` are blocked while removal is incomplete. Repeating removal after completion succeeds without changes.

## Failure and cleanup behavior

Operations are serialized per workspace. If provisioning stops, fix the error and repeat the original command; completed repositories are preserved and skipped. Intact incomplete worktrees are recreated on retry so setup hooks run again. Worktrees with changes are preserved for inspection. Use `wmm remove <branch>` to abandon the entire change.

Worktrees remain compatible with ordinary workmux commands run from their real directories. Apart from the internal coordination repository's empty initialization commit, wmm does not commit, push, merge, or call agent APIs itself; interactive agents act under their own permission settings.

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
