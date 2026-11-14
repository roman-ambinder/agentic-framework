package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/evals"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/docker"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxset"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
)

// evalOptions are the flags of `adlc eval`.
type evalOptions struct {
	suiteDir, db, out, workDir, image string
	agents, bugs                      []string
	keepWorkspaces, dryRun            bool
	repeat                            int
}

func newEvalCmd() *cobra.Command {
	var o evalOptions
	cmd := &cobra.Command{
		Use:   "eval SUITE_DIR --agents FILE[,FILE...]",
		Short: "Run agents on the seeded bugs of an eval suite",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.suiteDir = args[0]
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runEval(ctx, o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringSliceVar(&o.agents, "agents", nil, "agent files, comma separated (required)")
	f.StringSliceVar(&o.bugs, "bugs", nil, "only these bug ids, comma separated (default: all)")
	f.StringVar(&o.db, "db", defaultDBPath, "event log file")
	f.StringVar(&o.out, "out", "", "report path (default: evals/results/<suite>-<UTC time>.json)")
	f.StringVar(&o.workDir, "workdir", "", "host directory for workspaces (default: a new temporary directory)")
	f.StringVar(&o.image, "image", evals.DefaultImage, "sandbox image")
	f.BoolVar(&o.keepWorkspaces, "keep-workspaces", false, "keep the workspace directory after the run")
	f.BoolVar(&o.dryRun, "dry-run", false, "load the suite and agents, print the worst-case spend and exit (no Docker, no model calls)")
	f.IntVar(&o.repeat, "repeat", 1, "attempts per bug and agent (1 to 20)")
	_ = cmd.MarkFlagRequired("agents")
	return cmd
}

// selectBugs returns the bugs named by ids in suite order, or all bugs.
func selectBugs(s *evals.Suite, ids []string) ([]evals.Bug, error) {
	if len(ids) == 0 {
		return s.Bugs, nil
	}
	var out []evals.Bug
	for _, id := range ids {
		b, ok := s.Bug(id)
		if !ok {
			return nil, fmt.Errorf("unknown bug %q", id)
		}
		out = append(out, b)
	}
	return out, nil
}

func loadAgents(paths []string) ([]*agentconfig.Agent, error) {
	agents := make([]*agentconfig.Agent, 0, len(paths))
	for _, p := range paths {
		a, err := agentconfig.Load(p)
		if err != nil {
			return nil, err
		}
		if a.Model.Provider == "fake" {
			return nil, fmt.Errorf("eval: agent %s: provider fake is not supported by adlc eval", p)
		}
		agents = append(agents, a)
	}
	return agents, nil
}

// runEval implements `adlc eval`.
func runEval(ctx context.Context, o evalOptions, stdout io.Writer) error {
	if o.repeat < 1 || o.repeat > 20 {
		return &exitError{code: 1, err: errors.New("eval: --repeat must be between 1 and 20")}
	}
	suite, agents, bugs, err := loadEvalInputs(o)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	if o.dryRun {
		return evalDryRun(stdout, bugs, agents, o.repeat)
	}
	if err := os.MkdirAll(filepath.Dir(o.db), 0o755); err != nil {
		return &exitError{code: 1, err: fmt.Errorf("eval: create db directory: %w", err)}
	}
	store, err := eventlog.Open(ctx, o.db)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer store.Close()
	backend, err := docker.New(ctx)
	if err != nil {
		fmt.Fprintf(stdout, "Docker is not available: %v\n", err)
		return &exitError{code: 1, err: err}
	}
	defer backend.Close()

	workDir, cleanup, err := evalWorkDir(o)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer cleanup()

	r := &evals.Runner{
		Suite: suite, Sandbox: backend, Log: store, Image: o.image, WorkDir: workDir,
		RunAgent: evalRunAgent(store), Stdout: stdout, Repeat: o.repeat,
	}
	started := time.Now()
	results, runErr := r.RunAll(ctx, bugs, agents)
	rep := evals.Report{Suite: suite.Name, StartedAt: started, FinishedAt: time.Now(), Results: results, Repeat: o.repeat}
	rep.Summary = evals.Summarize(rep.Results)
	out := o.out
	if out == "" {
		out = filepath.Join("evals", "results", fmt.Sprintf("%s-%s.json", suite.Name, started.UTC().Format("20060102-150405")))
	}
	if err := evals.WriteReport(out, rep); err != nil {
		return &exitError{code: 1, err: err}
	}
	evals.PrintTable(stdout, rep)
	fmt.Fprintf(stdout, "report: %s\n", out)
	if runErr != nil {
		return &exitError{code: 1, err: runErr}
	}
	return nil
}

// evalDryRun prints "bugs=<n> agents=<m> repeat=<r> worst_case_usd=<x>", where
// the worst case is bugs x repeat x the sum of every agent's budget.max_usd. An agent without
// max_usd has no spend limit, so the dry run fails.
func evalDryRun(stdout io.Writer, bugs []evals.Bug, agents []*agentconfig.Agent, repeat int) error {
	var perBug int64
	for _, a := range agents {
		micros, _ := agentconfig.ParseMicroUSD(a.Budget.MaxUSD)
		if micros == 0 {
			return &exitError{code: 1, err: fmt.Errorf("eval: agent %s has no budget.max_usd, worst case is unbounded", a.AgentID())}
		}
		perBug += micros
	}
	total := perBug * int64(len(bugs)) * int64(repeat)
	fmt.Fprintf(stdout, "bugs=%d agents=%d repeat=%d worst_case_usd=%d.%06d\n", len(bugs), len(agents), repeat, total/1_000_000, total%1_000_000)
	return nil
}

// loadEvalInputs loads and checks everything that needs no Docker.
func loadEvalInputs(o evalOptions) (*evals.Suite, []*agentconfig.Agent, []evals.Bug, error) {
	suite, err := evals.LoadSuite(o.suiteDir)
	if err != nil {
		return nil, nil, nil, err
	}
	agents, err := loadAgents(o.agents)
	if err != nil {
		return nil, nil, nil, err
	}
	bugs, err := selectBugs(suite, o.bugs)
	if err != nil {
		return nil, nil, nil, err
	}
	return suite, agents, bugs, nil
}

// evalWorkDir returns the workspace directory and a cleanup function. A
// directory this command created is removed unless --keep-workspaces is set.
func evalWorkDir(o evalOptions) (string, func(), error) {
	if o.workDir != "" {
		if err := os.MkdirAll(o.workDir, 0o755); err != nil {
			return "", nil, fmt.Errorf("eval: create workdir: %w", err)
		}
		return o.workDir, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "adlc-eval-")
	if err != nil {
		return "", nil, fmt.Errorf("eval: create workdir: %w", err)
	}
	if o.keepWorkspaces {
		return dir, func() {}, nil
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// evalSandboxImage maps the image an eval sandbox runs to the value recorded
// in run_started; nil when the runner gave no image reference.
func evalSandboxImage(ar evals.AgentRun) *adlcv1.SandboxImage {
	if ar.ImageRef == "" {
		return nil
	}
	return &adlcv1.SandboxImage{Ref: ar.ImageRef, Id: ar.ImageID}
}

// evalRunAgent returns the RunAgent that runs the real harness with the
// sandbox tools. Model, config and policy come from the same helpers as
// `adlc run`.
func evalRunAgent(log harness.Log) func(context.Context, evals.AgentRun) (*eventlog.RunState, error) {
	return func(ctx context.Context, ar evals.AgentRun) (*eventlog.RunState, error) {
		a := ar.Agent
		if a.Model.Provider == "fake" {
			return nil, errors.New("eval: provider fake is not supported")
		}
		m, err := buildModel(a, "")
		if err != nil {
			return nil, err
		}
		pm, err := buildPlanModel(a, "")
		if err != nil {
			return nil, err
		}
		cfg := harnessConfig(a, ar.RunID, ar.Bug.Issue)
		cfg.SandboxImage = evalSandboxImage(ar)
		tools, err := sandboxset.NewRouter(ctx, sandboxtool.Options{Sandbox: ar.Sandbox, Handle: ar.Handle})
		if err != nil {
			return nil, fmt.Errorf("eval: start sandbox tools: %w", err)
		}
		defer func() { _ = tools.Close() }()
		return harness.Run(ctx, cfg, harness.Deps{Log: log, Model: m, PlanModel: pm, Tools: tools, Policy: a.PolicyEngine()})
	}
}
