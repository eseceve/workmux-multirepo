# workmux-multirepo

Coordinates work on one change that may span several Git repositories, moving it through debugging, implementation, and review with one tmux window per change.

## Language

### Places

**Root directory**:
The directory containing `wmm.toml` and the original repositories it maps.
_Avoid_: workspace, project root

**Workspace**:
The per-change directory under the root directory's `.wmm/` that holds the change's state and links to each of its worktrees by alias.
_Avoid_: coordination directory, change directory

**Repository alias**:
The short name `wmm.toml` gives a repository, used on the command line and as the link name inside a workspace.
_Avoid_: repo name, key

### Work

**Change**:
One unit of work identified by a branch name, spanning a fixed set of repositories chosen when it is first started.
_Avoid_: task, feature, workspace

**Phase**:
The stage a change is in: debug, implementation, or review.
_Avoid_: status, state

**Debug**:
The phase in which a change is investigated from the root directory, before any worktree exists.
_Avoid_: investigation, triage

**Change window**:
The single tmux window that belongs to a change; each phase replaces it rather than opening another.
_Avoid_: managed window, session

### Agents

**Builder**:
The agent that implements a change: inside the worktree for a single-repository change, or in the workspace when the change spans several repositories.
_Avoid_: implementer, implementation agent

**Reviewer**:
An agent that reviews one repository of a change from that repository's worktree.
_Avoid_: review agent
