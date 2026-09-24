# Agents are launched only by workmux

Wmm never builds agent commands or reads workmux's configuration; builders and reviewers come from workmux's panes and `agent` settings, and debug windows open a plain shell because workmux needs a worktree to launch an agent. Wmm only chooses which file workmux reads, passing it unchanged as `workmux --config`: a multi-repository builder uses the root directory's `.workmux.yaml`, and reviewers use `review_workmux_config` (flag, then `~/.config/wmm/config.toml`) so reviews can use a different agent or layout than implementation.

## Consequences

- `workmux --config` merges with the global configuration, so leaving `review_workmux_config` unset still opens the agent the normal configuration defines; a review without agents needs a file whose `panes` have no agent pane.
- `review_agent` and `build_command` stay rejected in `wmm.toml`.
