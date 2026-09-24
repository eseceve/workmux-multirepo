# One change window, replaced on every phase

A change keeps a single tmux window for its whole life: `wmm start` replaces the debug window and `wmm review` replaces the builder's window, closing the previous occupant, so several parallel changes stay one window each. The builder is closed during review on purpose; reviewers fix findings in their worktrees, and rerunning `wmm start` brings the builder back.

## Consequences

- Workmux can only open windows, not panes, so the new window is created by workmux and moved into the old window's position with `tmux swap-window`. Moving workmux's panes into the old window with `join-pane` was rejected because it discards the pane sizes from the workmux configuration.
- The command usually runs inside the window it replaces, so closing that window must be the last step, after the manifest is saved.

## Considered Options

- A separate review window beside the builder keeps the builder alive for findings that cross repositories, at the cost of two windows per change.
