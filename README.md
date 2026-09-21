# workmux-multirepo

A CLI built on top of [workmux](https://github.com/raine/workmux) for coordinated changes across multiple repositories. Suggested shell alias: `wmm`.

Independent project; not an official workmux plugin.

## Status

Planning. The CLI is not implemented yet. Commands below describe the proposed interface.

## Workflow

1. Create one worktree per selected repository, using the same branch name. Fetch each repository's origin and branch from its remote default branch.
2. Open one shared workspace with a single agent to implement the complete change across repositories.
3. Open one reviewer agent per repository on the existing worktrees, with the shared objective and cross-repository contracts as context. Pause implementation edits during review.
4. Validate integration across repositories and prepare a separate pull request for each repository.

A workspace manifest will record repository paths and base commits so the change can be reopened and reviewed against its original starting points.

## Proposed usage

```sh
alias wmm=workmux-multirepo

wmm start feat/my-change api backend frontend
wmm review feat/my-change
wmm status feat/my-change
```

Repository aliases and paths will be defined in workspace configuration. Workmux will provide per-repository capabilities, with additional orchestration for the shared implementation workspace.
