package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
)

// forkOptions are the flags of `adlc fork`.
type forkOptions struct {
	runOptions // agentPath, db, controlDB, fakeScript, runID, modelName, agentDir, workspaceOut, keepSandbox, startHook
	atStep     uint32
}

func newForkCmd() *cobra.Command {
	var o forkOptions
	cmd := &cobra.Command{
		Use:   "fork RUN_ID AGENT_YAML|NAME@N|NAME@ENV --at-step N",
		Short: "Start a new run from a run's transcript up to a step, with another agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.agentPath = args[1]
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return forkAgentWith(ctx, o, args[0], cmd.OutOrStdout(), openDockerRunBackend)
		},
	}
	f := cmd.Flags()
	f.Uint32Var(&o.atStep, "at-step", 0, "step of RUN_ID whose checkpoint the new run continues from (required)")
	f.StringVar(&o.runID, "run-id", "", "run id of the new run (default: generated)")
	f.StringVar(&o.db, "db", defaultDBPath, "event log file")
	f.StringVar(&o.controlDB, "control-db", defaultControlDBPath, "control database (kill switches)")
	f.StringVar(&o.fakeScript, "fake-script", "", "fake model script (provider fake only)")
	f.StringVar(&o.modelName, "model-name", "", "overrides model.name from the agent file")
	f.StringVar(&o.agentDir, "agent-dir", "", "for NAME@N or NAME@ENV: directory that tool commands, hooks and sandbox.workspace are resolved against (default: the directory the version was registered from)")
	f.StringVar(&o.workspaceOut, "workspace-out", "", "where to copy the sandbox workspace after the run (default .adlc/workspaces/<run-id>)")
	f.BoolVar(&o.keepSandbox, "keep-sandbox", false, "do not destroy the sandbox container after the run")
	_ = cmd.MarkFlagRequired("at-step")
	return cmd
}

// forkAgentWith forks run originID at o.atStep, continues the new run with the
// agent and prints the summary to stdout. Everything that can be checked
// without side effects is checked before a sandbox or a tool starts.
func forkAgentWith(ctx context.Context, o forkOptions, originID string, stdout io.Writer, openSandbox openRunBackend) error {
	if o.atStep == 0 {
		return &exitError{code: 1, err: fmt.Errorf("fork: --at-step must be at least 1")}
	}
	ro, cleanupRef, err := resolveAgentRef(ctx, o.runOptions)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer cleanupRef()
	o.runOptions = ro
	a, err := loadAgent(o.runOptions)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	runID := o.runID
	if runID == "" {
		if runID, err = newRunID(time.Now(), rand.Reader); err != nil {
			return &exitError{code: 1, err: err}
		}
	} else if !validRunID(runID) {
		return &exitError{code: 1, err: fmt.Errorf("fork: invalid --run-id %q", runID)}
	}
	store, err := openExistingLog(ctx, o.db)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer store.Close()
	copied, err := checkFork(ctx, store, originID, runID, o.atStep)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
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
	if sb != nil {
		fmt.Fprintf(stdout, "warning: the sandbox starts from the agent file's workspace; workspace changes of run %s up to step %d are not restored\n", originID, o.atStep)
	}
	cfg, deps, cleanup, err := buildRun(ctx, a, o.runOptions, runID, store)
	if err != nil {
		sb.destroy(ctx, stdout)
		return &exitError{code: 1, err: err}
	}
	defer func() { _ = cleanup() }()
	deps.KillSwitch = ks
	state, err := harness.Fork(ctx, cfg, deps, harness.ForkPoint{RunID: originID, AtStep: o.atStep})
	if err != nil {
		sb.keepForResume(stdout)
		return &exitError{code: 1, err: err}
	}
	fmt.Fprintf(stdout, "%-9s%s at step %d (%d events copied)\n", "forked:", originID, o.atStep, copied)
	printSummary(stdout, state)
	if err := sb.finish(ctx, o.runOptions, store, state, stdout); err != nil {
		return &exitError{code: 1, err: err}
	}
	if state.StopReason == adlcv1.StopReason_STOP_REASON_COMPLETED {
		return nil
	}
	return &exitError{code: 2}
}

// checkFork refuses a fork that harness.Fork would refuse, and returns the
// number of events the fork copies after its run_started.
func checkFork(ctx context.Context, store *eventlog.Store, originID, runID string, step uint32) (int, error) {
	origin, err := store.Read(ctx, originID, 0)
	if err != nil {
		return 0, fmt.Errorf("fork: %w", err)
	}
	if len(origin) == 0 {
		return 0, fmt.Errorf("fork: %w: %q", eventlog.ErrRunNotFound, originID)
	}
	prefix, err := harness.ForkEvents(origin, step)
	if err != nil {
		return 0, fmt.Errorf("fork: %w", err)
	}
	existing, err := store.Read(ctx, runID, 0)
	if err != nil {
		return 0, fmt.Errorf("fork: %w", err)
	}
	if len(existing) > 0 {
		return 0, fmt.Errorf("fork: %w: %q", harness.ErrRunExists, runID)
	}
	return len(prefix) - 1, nil
}
