package cli

import (
	"bytes"
	"fmt"
	"github.com/spf13/cobra"
	"strings"
)

// Version is injected into release binaries.
var Version = "dev"

type options struct {
	command, config, branch, prompt, fields string
	repos                                   []string
	dryRun, noOpen, preparePR, help, fetch  bool
	helpText                                string
}

func newRoot(o *options) *cobra.Command {
	root := &cobra.Command{
		Use: "wmm", Short: "Coordinate one implementation agent and per-repository reviews",
		SilenceUsage: true, SilenceErrors: true, Version: Version,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { o.command = "status"; return nil },
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&o.config, "config", "", "wmm.toml path (default: search current directory and parents)")
	initCmd := &cobra.Command{Use: "init", Short: "Create configuration from child repositories", Args: cobra.NoArgs,
		Example: "  wmm init\n  wmm init --dry-run", RunE: func(cmd *cobra.Command, args []string) error { o.command = "init"; return nil }}
	initCmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "show discovered repositories without writing")
	startCmd := &cobra.Command{Use: "start <branch> <repos...>", Short: "Create or reopen a shared implementation workspace", Args: cobra.MinimumNArgs(2),
		Example: "  wmm start feat/change api web --prompt 'Add checkout'\n  wmm start feat/change api web --dry-run",
		RunE: func(cmd *cobra.Command, args []string) error {
			o.command = "start"
			o.branch = args[0]
			o.repos = args[1:]
			return nil
		}}
	startCmd.Flags().StringVar(&o.prompt, "prompt", "", "optional initial prompt (default: none)")
	startCmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "show plan without fetching, creating, or launching")
	startCmd.Flags().BoolVar(&o.noOpen, "no-open", false, "provision worktrees without opening a window")
	startCmd.Flags().BoolVar(&o.fetch, "fetch", false, "fetch remote default bases before creating a new workspace (reopening keeps pinned bases)")
	reviewCmd := &cobra.Command{Use: "review <branch>", Short: "Open reviewer panes per repository in one tmux window", Args: cobra.ExactArgs(1),
		Example: "  wmm review feat/change\n  wmm review feat/change --prepare-pr --dry-run",
		RunE:    func(cmd *cobra.Command, args []string) error { o.command = "review"; o.branch = args[0]; return nil }}
	reviewCmd.Flags().BoolVar(&o.preparePR, "prepare-pr", false, "draft PR text locally; never publish automatically")
	reviewCmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "show review targets without opening agents")
	statusCmd := &cobra.Command{Use: "status [branch]", Short: "Show changes or repository state for one change", Args: cobra.MaximumNArgs(1),
		Example: "  wmm status\n  wmm status feat/change --fields repo,state,path",
		RunE: func(cmd *cobra.Command, args []string) error {
			o.command = "status"
			if len(args) > 0 {
				o.branch = args[0]
			}
			return nil
		}}
	statusCmd.Flags().StringVar(&o.fields, "fields", "repo,state,commits", "detail columns: repo,state,commits,base,path,base_commit")
	root.AddCommand(initCmd, startCmd, reviewCmd, statusCmd)
	return root
}

func parse(args []string) (options, error) {
	var o options
	root := newRoot(&o)
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(args)
	cmd, err := root.ExecuteC()
	if err != nil {
		if cmd == nil {
			cmd = root
		}
		return o, usage(err.Error(), cmd.UsageString())
	}
	if output.Len() > 0 {
		o.help = true
		o.helpText = output.String()
	}
	return o, nil
}
func help(command string) string {
	var o options
	root := newRoot(&o)
	cmd, _, err := root.Find(strings.Fields(command))
	if err != nil {
		return root.UsageString()
	}
	return cmd.UsageString()
}

func printHelp(o options) string {
	if o.helpText != "" {
		return o.helpText
	}
	return fmt.Sprintln(help(o.command))
}
