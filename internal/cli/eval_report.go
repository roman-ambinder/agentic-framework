package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/roman-ambinder/agentic-framework/internal/evals"
)

func newEvalReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "eval-report FILE",
		Short: "Print the table of a report written by `adlc eval`",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := readReport(args[0])
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			evals.PrintTable(cmd.OutOrStdout(), rep)
			return nil
		},
	}
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
