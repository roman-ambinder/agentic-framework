package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/controlplane"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Register, list, show and promote agent versions",
	}
	cmd.PersistentFlags().String("control-db", defaultControlDBPath, "control database (agent registry)")
	cmd.AddCommand(newAgentRegisterCmd(), newAgentListCmd(), newAgentShowCmd(), newAgentPromoteCmd())
	return cmd
}

// controlDBFlag returns the value of the persistent --control-db flag.
func controlDBFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("control-db")
	return v
}

func newAgentRegisterCmd() *cobra.Command {
	var modelName string
	cmd := &cobra.Command{
		Use:   "register AGENT_YAML",
		Short: "Store an agent file as an immutable version (name@N)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := loadAgent(runOptions{agentPath: args[0], modelName: modelName})
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			store, err := openControlDB(cmd.Context(), controlDBFlag(cmd))
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			defer store.Close()
			v, created, err := store.Register(cmd.Context(), a)
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			verb := "already registered as"
			if created {
				verb = "registered"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%s)\n", verb, v.Ref(), v.Digest)
			return nil
		},
	}
	cmd.Flags().StringVar(&modelName, "model-name", "", "overrides model.name from the agent file")
	return cmd
}

func newAgentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every registered version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := controlDBFlag(cmd)
			w := cmd.OutOrStdout()
			if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
				fmt.Fprint(w, "no registered agents\n")
				return nil
			}
			store, err := controlplane.Open(cmd.Context(), path)
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			defer store.Close()
			vs, err := store.Versions(cmd.Context())
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			return printVersions(w, vs)
		},
	}
}

// printVersions prints the table of `adlc agent list`.
func printVersions(w io.Writer, vs []controlplane.AgentVersion) error {
	if len(vs) == 0 {
		fmt.Fprint(w, "no registered agents\n")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprint(tw, "VERSION\tAGENT_ID\tDIGEST\tENVS\tREGISTERED\n")
	for _, v := range vs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", v.Ref(), v.AgentID, v.Digest[:min(19, len(v.Digest))],
			envList(v.Envs), v.CreatedAt.UTC().Format(time.RFC3339))
	}
	return tw.Flush()
}

// envList joins envs with "," or returns "-" when there are none.
func envList(envs []controlplane.Env) string {
	if len(envs) == 0 {
		return "-"
	}
	names := make([]string, len(envs))
	for i, e := range envs {
		names[i] = string(e)
	}
	return strings.Join(names, ",")
}

func newAgentShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME@N|NAME@ENV",
		Short: "Show one registered version and its files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := controlplane.ParseRef(args[0])
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			store, err := openExistingControlDB(cmd.Context(), controlDBFlag(cmd))
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			defer store.Close()
			v, err := store.Resolve(cmd.Context(), ref)
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			printVersion(cmd.OutOrStdout(), v)
			return nil
		},
	}
}

func printVersion(w io.Writer, v controlplane.AgentVersion) {
	fmt.Fprintf(w, "%-12s%s\n", "version:", v.Ref())
	fmt.Fprintf(w, "%-12s%s\n", "agent id:", v.AgentID)
	fmt.Fprintf(w, "%-12s%s\n", "digest:", v.Digest)
	fmt.Fprintf(w, "%-12s%s\n", "registered:", v.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "%-12s%s\n", "source dir:", v.SourceDir)
	fmt.Fprintf(w, "%-12s%s\n", "envs:", envList(v.Envs))
	for _, f := range v.Files {
		fmt.Fprintf(w, "%-12s%s (%d bytes)\n", "file:", f.Path, len(f.Data))
	}
}

// promoteOptions are the flags of `adlc agent promote`.
type promoteOptions struct {
	to, report, maxMedianUSD string
	minPassAt1               float64
}

func newAgentPromoteCmd() *cobra.Command {
	var o promoteOptions
	cmd := &cobra.Command{
		Use:   "promote NAME@N --to staging|prod --report FILE",
		Short: "Promote a version to an environment if an eval report proves it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := promoteAgent(cmd.Context(), controlDBFlag(cmd), o, args[0], cmd.OutOrStdout()); err != nil {
				return &exitError{code: 1, err: err}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.to, "to", "", "environment: staging or prod (required)")
	f.StringVar(&o.report, "report", "", "eval report written by `adlc eval` (required)")
	f.Float64Var(&o.minPassAt1, "min-pass-at-1", 0.5, "minimum pass@1 (0..1)")
	f.StringVar(&o.maxMedianUSD, "max-median-usd", "", "maximum median cost of solved runs in USD (default: no limit)")
	_ = cmd.MarkFlagRequired("to")
	_ = cmd.MarkFlagRequired("report")
	return cmd
}

func promoteAgent(ctx context.Context, controlDB string, o promoteOptions, arg string, w io.Writer) error {
	ref, err := controlplane.ParseRef(arg)
	if err != nil {
		return err
	}
	if ref.Env != "" {
		return fmt.Errorf("agent promote: want NAME@N, got %q", arg)
	}
	env, err := controlplane.ParseEnv(o.to)
	if err != nil {
		return err
	}
	gate := controlplane.Gate{MinPassAt1: o.minPassAt1, MaxMedianUSDMicros: -1}
	if o.maxMedianUSD != "" {
		if gate.MaxMedianUSDMicros, err = agentconfig.ParseMicroUSD(o.maxMedianUSD); err != nil {
			return fmt.Errorf("agent promote: --max-median-usd: %w", err)
		}
	}
	rep, err := readReport(o.report)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(o.report)
	if err != nil {
		return fmt.Errorf("agent promote: %w", err)
	}
	store, err := openExistingControlDB(ctx, controlDB)
	if err != nil {
		return err
	}
	defer store.Close()
	p, err := store.Promote(ctx, ref.Name, ref.Version, env, rep, abs, gate)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "promoted %s@%d to %s (pass@1 %.3f over %d runs, median $%s)\n",
		p.Name, p.Version, p.Env, p.PassAt1, p.Runs, formatUSD(p.MedianUSDMicros))
	printPromotionImage(w, p)
	return nil
}

// printPromotionImage prints the image the promotion's evidence ran on, or a
// warning when the report does not say.
func printPromotionImage(w io.Writer, p controlplane.Promotion) {
	if p.ImageID == "" {
		fmt.Fprintln(w, "warning: the report does not say which image the results used")
		return
	}
	fmt.Fprintf(w, "image: %s\n", sandbox.ShortImageID(p.ImageID))
	if p.ResultsWithoutImage > 0 {
		fmt.Fprintf(w, "warning: %d of %d results do not say which image they used\n", p.ResultsWithoutImage, p.ResultsChecked)
	}
}
