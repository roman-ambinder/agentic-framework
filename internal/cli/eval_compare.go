package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/roman-ambinder/agentic-framework/internal/evals"
)

func newEvalCompareCmd() *cobra.Command {
	var maps []string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "eval-compare BASE.json NEW.json [--map BASE_AGENT=NEW_AGENT ...] [--json]",
		Short: "Compare two reports written by `adlc eval`",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runEvalCompare(cmd, args, maps, asJSON); err != nil {
				return &exitError{code: 1, err: err}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&maps, "map", nil, "compare these agent ids, BASE_AGENT=NEW_AGENT (repeatable; default: match by agent name)")
	f.BoolVar(&asJSON, "json", false, "print the comparison as JSON")
	return cmd
}

func runEvalCompare(cmd *cobra.Command, args, maps []string, asJSON bool) error {
	mapping, err := parseAgentMaps(maps)
	if err != nil {
		return err
	}
	base, err := readReport(args[0])
	if err != nil {
		return err
	}
	newRep, err := readReport(args[1])
	if err != nil {
		return err
	}
	c, err := evals.Compare(base, newRep, mapping)
	if err != nil {
		return err
	}
	c.Base.Path, c.New.Path = args[0], args[1]
	if !asJSON {
		evals.PrintComparison(cmd.OutOrStdout(), c)
		return nil
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("eval-compare: encode: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", data)
	return err
}

// parseAgentMaps parses --map values of the form BASE=NEW.
func parseAgentMaps(values []string) ([]evals.AgentPair, error) {
	var pairs []evals.AgentPair
	for _, v := range values {
		b, n, _ := strings.Cut(v, "=")
		if b == "" || n == "" {
			return nil, fmt.Errorf("eval-compare: --map %q: want BASE_AGENT=NEW_AGENT", v)
		}
		pairs = append(pairs, evals.AgentPair{Base: b, New: n})
	}
	return pairs, nil
}
