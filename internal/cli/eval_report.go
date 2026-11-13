package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/roman-ambinder/agentic-framework/internal/evals"
)

func newEvalReportCmd() *cobra.Command {
	var failures, asJSON bool
	var db string
	cmd := &cobra.Command{
		Use:   "eval-report FILE",
		Short: "Print the table of a report written by `adlc eval`",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if asJSON && !failures {
				return &exitError{code: 1, err: errors.New("eval-report: --json needs --failures")}
			}
			rep, err := readReport(args[0])
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			if failures {
				return printFailures(cmd, rep, db, asJSON)
			}
			evals.PrintTable(cmd.OutOrStdout(), rep)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&failures, "failures", false, "label every unsolved result with a failure cause (reads the event log)")
	f.StringVar(&db, "db", defaultDBPath, "event log file (with --failures)")
	f.BoolVar(&asJSON, "json", false, "print the failure analysis as JSON (with --failures)")
	return cmd
}

// printFailures classifies the unsolved results of rep from their events.
func printFailures(cmd *cobra.Command, rep evals.Report, db string, asJSON bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	store, err := openExistingLog(ctx, db)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer store.Close()
	fr, err := evals.AnalyzeFailures(ctx, rep, store)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	out := cmd.OutOrStdout()
	if asJSON {
		data, err := json.MarshalIndent(fr, "", "  ")
		if err != nil {
			return &exitError{code: 1, err: fmt.Errorf("eval-report: encode failures: %w", err)}
		}
		_, _ = out.Write(append(data, '\n'))
		return nil
	}
	evals.PrintTable(out, rep)
	fmt.Fprintln(out)
	evals.PrintFailures(out, fr)
	return nil
}

// readReport loads a report JSON file.
func readReport(path string) (evals.Report, error) {
	var rep evals.Report
	data, err := os.ReadFile(path)
	if err != nil {
		return rep, fmt.Errorf("read report %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		return rep, fmt.Errorf("read report %s: %w", path, err)
	}
	return rep, nil
}
