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
	keepWorkspaces                    bool
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
	f.StringVar(&o.db, "db", ".adlc/events.db", "event log file")
	f.StringVar(&o.out, "out", "", "report path (default: evals/results/<suite>-<UTC time>.json)")
	f.StringVar(&o.workDir, "workdir", "", "host directory for workspaces (default: a new temporary directory)")
	f.StringVar(&o.image, "image", evals.DefaultImage, "sandbox image")
	f.BoolVar(&o.keepWorkspaces, "keep-workspaces", false, "keep the workspace directory after the run")
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
	suite, agents, bugs, err := loadEvalInputs(o)
	if err != nil {
		return &exitError{code: 1, err: err}
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
		RunAgent: evalRunAgent(store), Stdout: stdout,
	}
	started := time.Now()
	results, runErr := r.RunAll(ctx, bugs, agents)
	rep := evals.Report{Suite: suite.Name, StartedAt: started, FinishedAt: time.Now(), Results: results}
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
		cfg := harnessConfig(a, ar.RunID, ar.Bug.Issue)
		tools, err := sandboxset.NewRouter(ctx, sandboxtool.Options{Sandbox: ar.Sandbox, Handle: ar.Handle})
		if err != nil {
			return nil, fmt.Errorf("eval: start sandbox tools: %w", err)
		}
		defer func() { _ = tools.Close() }()
		return harness.Run(ctx, cfg, harness.Deps{Log: log, Model: m, Tools: tools, Policy: a.PolicyEngine()})
	}
}
