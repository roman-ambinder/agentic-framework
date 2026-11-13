// Package cli wires the adlc subcommands together.
package cli

import (
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
	return root
}

// Execute runs the CLI with args and returns a process exit code.
func Execute(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		root.PrintErrln("Error:", err)
		return 1
	}
	return 0
}
