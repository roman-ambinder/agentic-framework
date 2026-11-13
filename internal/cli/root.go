// Package cli wires the adlc subcommands together.
package cli

import (
	"errors"
	"io"

	"github.com/spf13/cobra"
)

// newRootCmd builds the command tree. Subcommands from later milestones
// (run, serve, approve, eval, replay, kill) are added here.
func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "adlc",
		Short:         "Agentic Development Life Cycle framework",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(newVersionCmd())
	root.AddCommand(newToolCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newCostCmd())
	root.AddCommand(newEvalCmd())
	root.AddCommand(newTraceCmd())
	root.AddCommand(newEvalReportCmd())
	root.AddCommand(newResumeCmd())
	return root
}

// Execute runs the CLI with args and returns a process exit code.
func Execute(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil {
				root.PrintErrln("Error:", ee.err)
			}
			return ee.code
		}
		root.PrintErrln("Error:", err)
		return 1
	}
	return 0
}
