package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/roman-ambinder/agentic-framework/internal/controlplane"
)

var errKillArgs = errors.New(`kill: want "run RUN_ID", "agent AGENT" or "all"`)

// parseKillArgs maps ["run", ID] / ["agent", AGENT] / ["all"] to a scope and target.
func parseKillArgs(args []string) (controlplane.Scope, string, error) {
	switch {
	case len(args) == 1 && args[0] == "all":
		return controlplane.ScopeAll, "", nil
	case len(args) == 2 && args[0] == "run":
		return controlplane.ScopeRun, args[1], nil
	case len(args) == 2 && args[0] == "agent":
		return controlplane.ScopeAgent, args[1], nil
	}
	return "", "", errKillArgs
}

// describeKill is the text for a kill in command output: "all" or "<scope> <target>".
func describeKill(scope controlplane.Scope, target string) string {
	if scope == controlplane.ScopeAll {
		return "all"
	}
	return string(scope) + " " + target
}

func newKillCmd() *cobra.Command {
	var controlDB, reason string
	cmd := &cobra.Command{
		Use:   "kill (run RUN_ID | agent AGENT | all) --reason TEXT",
		Short: "Stop one run, every run of an agent, or every run",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, target, err := parseKillArgs(args)
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			if err := setKill(cmd.Context(), controlDB, scope, target, reason); err != nil {
				return &exitError{code: 1, err: err}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "kill set: %s (reason: %s)\n", describeKill(scope, target), reason)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&reason, "reason", "", "why the run is stopped (required)")
	f.StringVar(&controlDB, "control-db", defaultControlDBPath, "control database (kill switches)")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func setKill(ctx context.Context, path string, scope controlplane.Scope, target, reason string) error {
	store, err := openControlDB(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Kill(ctx, scope, target, reason)
}

func newUnkillCmd() *cobra.Command {
	var controlDB string
	cmd := &cobra.Command{
		Use:   "unkill (run RUN_ID | agent AGENT | all)",
		Short: "Remove a kill",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, target, err := parseKillArgs(args)
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			if err := removeKill(cmd.Context(), controlDB, scope, target); err != nil {
				return &exitError{code: 1, err: err}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "kill removed: %s\n", describeKill(scope, target))
			return nil
		},
	}
	cmd.Flags().StringVar(&controlDB, "control-db", defaultControlDBPath, "control database (kill switches)")
	return cmd
}

func removeKill(ctx context.Context, path string, scope controlplane.Scope, target string) error {
	store, err := openExistingControlDB(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Unkill(ctx, scope, target)
}

func newKillsCmd() *cobra.Command {
	var controlDB string
	cmd := &cobra.Command{
		Use:   "kills",
		Short: "List the active kills",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := listKills(cmd.Context(), controlDB, cmd.OutOrStdout()); err != nil {
				return &exitError{code: 1, err: err}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&controlDB, "control-db", defaultControlDBPath, "control database (kill switches)")
	return cmd
}

// listKills prints the active kills; a missing control db has none (and is not created).
func listKills(ctx context.Context, path string, w io.Writer) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprint(w, "no active kills\n")
		return nil
	}
	store, err := controlplane.Open(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()
	kills, err := store.Active(ctx)
	if err != nil {
		return err
	}
	if len(kills) == 0 {
		fmt.Fprint(w, "no active kills\n")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "SCOPE\tTARGET\tREASON\tSINCE\n")
	for _, k := range kills {
		target := k.Target
		if k.Scope == controlplane.ScopeAll {
			target = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k.Scope, target, k.Reason, k.CreatedAt.UTC().Format(time.RFC3339))
	}
	return tw.Flush()
}
