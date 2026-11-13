package cli

import (
	"context"
	"crypto/rand"
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
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
)

// runOptions are the flags of `adlc run`.
type runOptions struct {
	agentPath, task, db, fakeScript, runID, modelName string
	// controlDB is the control database (kill switches); a missing file means no kill switch.
	controlDB string
	// startHook starts one hook plugin; nil means startPluginHook. Tests set it.
	startHook hookStarter
	// workspaceOut and keepSandbox only apply to `adlc run` with a sandbox.
	workspaceOut string
	keepSandbox  bool
}

func newRunCmd() *cobra.Command {
	var o runOptions
	cmd := &cobra.Command{
		Use:   "run AGENT_YAML --task TEXT",
		Short: "Run an agent on a task and print a summary",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.agentPath = args[0]
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runAgent(ctx, o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.task, "task", "", "first user message (required)")
	f.StringVar(&o.db, "db", defaultDBPath, "event log file")
	f.StringVar(&o.controlDB, "control-db", defaultControlDBPath, "control database (kill switches)")
	f.StringVar(&o.fakeScript, "fake-script", "", "fake model script (provider fake only)")
	f.StringVar(&o.runID, "run-id", "", "run id (default: generated)")
	f.StringVar(&o.modelName, "model-name", "", "overrides model.name from the agent file")
	f.StringVar(&o.workspaceOut, "workspace-out", "", "where to copy the sandbox workspace after the run (default .adlc/workspaces/<run-id>)")
	f.BoolVar(&o.keepSandbox, "keep-sandbox", false, "do not destroy the sandbox container after the run")
	_ = cmd.MarkFlagRequired("task")
	return cmd
}

// loadAgent loads the agent file; --model-name stands in for an unset
// ${ADLC_..._MODEL} variable during the load only.
func loadAgent(o runOptions) (*agentconfig.Agent, error) {
	if o.modelName != "" {
		for _, v := range []string{"ADLC_ANTHROPIC_MODEL", "ADLC_OPENAI_MODEL"} {
			if os.Getenv(v) == "" {
				_ = os.Setenv(v, o.modelName)
				defer os.Unsetenv(v)
			}
		}
	}
	a, err := agentconfig.Load(o.agentPath)
	if err != nil {
		return nil, err
	}
	if o.modelName != "" {
		a.Model.Name = o.modelName
	}
	return a, nil
}

// runAgent runs the agent once and prints the summary to stdout. A sandbox,
// if the agent declares one, comes from the Docker backend.
func runAgent(ctx context.Context, o runOptions, stdout io.Writer) error {
	return runAgentWith(ctx, o, stdout, openDockerRunBackend)
}

// runAgentWith is runAgent with an injectable sandbox backend.
func runAgentWith(ctx context.Context, o runOptions, stdout io.Writer, openSandbox openRunBackend) error {
	a, err := loadAgent(o)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	runID := o.runID
	if runID == "" {
		if runID, err = newRunID(time.Now(), rand.Reader); err != nil {
			return &exitError{code: 1, err: err}
		}
	} else if !validRunID(runID) {
		return &exitError{code: 1, err: fmt.Errorf("run: invalid --run-id %q", runID)}
	}
	if err := os.MkdirAll(filepath.Dir(o.db), 0o755); err != nil {
		return &exitError{code: 1, err: fmt.Errorf("run: create db directory: %w", err)}
	}
	store, err := eventlog.Open(ctx, o.db)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer store.Close()
	ks, closeKS, err := openKillSwitch(ctx, o.controlDB)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer closeKS()
	sb, err := startRunSandbox(ctx, a, runID, openSandbox)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer sb.close()
	cfg, deps, cleanup, err := buildRun(ctx, a, o, runID, store)
	if err != nil {
		sb.destroy(ctx, stdout)
		return &exitError{code: 1, err: err}
	}
	defer func() { _ = cleanup() }()
	deps.KillSwitch = ks
	state, err := harness.Run(ctx, cfg, deps)
	if err != nil {
		sb.keepForResume(stdout)
		return &exitError{code: 1, err: err}
	}
	printSummary(stdout, state)
	if err := sb.finish(ctx, o, state, stdout); err != nil {
		return &exitError{code: 1, err: err}
	}
	if state.StopReason == adlcv1.StopReason_STOP_REASON_COMPLETED {
		return nil
	}
	return &exitError{code: 2}
}
