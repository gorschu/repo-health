// Package cli implements repo-health's command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/gorschu/repo-health/internal/health"
	"github.com/spf13/cobra"
)

// BuildInfo identifies the executable's release and source revision.
type BuildInfo struct{ Version, Commit, Date string }

type operationError struct{ err error }

func (e operationError) Error() string { return e.err.Error() }
func (e operationError) Unwrap() error { return e.err }
func operation(err error) error {
	if err != nil {
		return operationError{err}
	}
	return nil
}

// Run returns 0 for success, 1 for operational errors and 2 for invalid usage.
func Run(ctx context.Context, args []string, out, errOut io.Writer, info BuildInfo) int {
	cmd := newRoot(info)
	cmd.SetArgs(args)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	if err := cmd.ExecuteContext(ctx); err != nil {
		message := strings.Map(func(r rune) rune {
			if !unicode.IsPrint(r) {
				return '?'
			}
			return r
		}, err.Error())
		if _, ok := errors.AsType[operationError](err); ok {
			fmt.Fprintln(errOut, "Repo check failed: "+message)
			return 1
		}
		fmt.Fprintln(errOut, "Error: "+message)
		fmt.Fprintln(errOut, "Run 'repo-health --help' for usage.")
		return 2
	}
	return 0
}

func newRoot(info BuildInfo) *cobra.Command {
	root := &cobra.Command{
		Use:           "repo-health",
		Short:         "Find local Git work across your Mani repositories",
		Long:          "Find local Git work across your Mani repositories.\n\nChecks staged and unstaged changes, untracked files, stashes, branches and\nlinked worktrees. Never fetches, pushes or changes your Git state.\n\nMANI_CONFIG selects the inventory; XDG_CACHE_HOME selects the cache root.",
		Version:       info.Version,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("repo-health {{.Version}}\n")
	root.AddCommand(&cobra.Command{
		Use: "scan", Short: "Refresh the cache from all Mani repositories", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return operation(health.Scan(cmd.Context())) },
	})

	var record bool
	inspect := &cobra.Command{
		Use: "inspect", Short: "Check this repository and its linked worktrees", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := ""
			if record {
				dir = os.Getenv("REPO_HEALTH_RUN_DIR")
				if dir == "" {
					return errors.New("--record requires a scan directory")
				}
			}
			path, err := os.Getwd()
			if err != nil {
				return operation(err)
			}
			text, err := health.Inspect(cmd.Context(), path, dir)
			if err != nil {
				return operation(err)
			}
			_, err = io.WriteString(cmd.OutOrStdout(), text)
			return operation(err)
		},
	}
	inspect.Flags().BoolVar(&record, "record", false, "Write an internal scan record")
	_ = inspect.Flags().MarkHidden("record")
	root.AddCommand(inspect)

	var severity string
	prompt := &cobra.Command{
		Use: "prompt", Short: "Print a compact cached reminder, or nothing when clean", Args: cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if !slices.Contains([]string{"", "error", "warning"}, severity) {
				return errors.New("--severity must be error or warning")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return showCached(cmd, "prompt", severity) },
	}
	prompt.Flags().StringVar(&severity, "severity", "", "Only show one severity: error or warning")
	_ = prompt.RegisterFlagCompletionFunc("severity", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"error", "warning"}, cobra.ShellCompDirectiveNoFileComp
	})
	root.AddCommand(prompt)
	for _, item := range []struct{ name, description string }{
		{"summary", "Print cached counts and report age"},
		{"show", "Print the detailed cached report"},
	} {
		root.AddCommand(&cobra.Command{
			Use: item.name, Short: item.description, Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error { return showCached(cmd, item.name, "") },
		})
	}
	root.AddCommand(&cobra.Command{
		Use: "version", Short: "Print the version and source revision", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "repo-health %s\ncommit: %s\ncommit date: %s\n", info.Version, info.Commit, info.Date)
			return operation(err)
		},
	})
	return root
}

func showCached(cmd *cobra.Command, mode, severity string) error {
	text, err := health.Display(mode, severity)
	if err != nil {
		return operation(err)
	}
	_, err = io.WriteString(cmd.OutOrStdout(), text)
	return operation(err)
}
