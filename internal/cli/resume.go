package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/docker"
)

// sandboxProbeTimeout bounds the "is the sandbox alive" check.
const sandboxProbeTimeout = 10 * time.Second

// sandboxLookup is the part of the sandbox backend `adlc resume` uses;
// *docker.Backend implements it.
type sandboxLookup interface {
	FindByRunID(ctx context.Context, runID string) ([]sandbox.Handle, error)
	Exec(ctx context.Context, h sandbox.Handle, cmd []string, timeout time.Duration) (sandbox.ExecResult, error)
	Close() error
}

// openSandboxBackend connects to the Docker backend.
func openSandboxBackend(ctx context.Context) (sandboxLookup, error) {
	b, err := docker.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("resume: connect to sandbox backend: %w", err)
	}
	return b, nil
}

func newResumeCmd() *cobra.Command {
	var o runOptions
	cmd := &cobra.Command{
		Use:   "resume RUN_ID --agent AGENT_YAML",
		Short: "Continue a run that stopped without run_completed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return resumeAgent(ctx, o, args[0], cmd.OutOrStdout(), openSandboxBackend)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.agentPath, "agent", "", "agent file the run was started with (required)")
	f.StringVar(&o.db, "db", ".adlc/events.db", "event log file")
	f.StringVar(&o.fakeScript, "fake-script", "", "fake model script (provider fake only)")
	f.StringVar(&o.modelName, "model-name", "", "overrides model.name from the agent file")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

// resumeAgent continues the run runID and prints the summary to stdout.
func resumeAgent(ctx context.Context, o runOptions, runID string, stdout io.Writer,
	openSandbox func(context.Context) (sandboxLookup, error),
) error {
	a, err := loadAgent(o)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	store, err := openExistingLog(ctx, o.db)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer store.Close()
	if err := checkResumable(ctx, store, runID); err != nil {
		return &exitError{code: 1, err: err}
	}
	if err := attachSandbox(ctx, a, runID, openSandbox); err != nil {
		return &exitError{code: 1, err: err}
	}
	cfg, deps, cleanup, err := buildRun(ctx, a, o, runID, store)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer func() { _ = cleanup() }()
	state, err := harness.Resume(ctx, cfg, deps)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	printSummary(stdout, state)
	if state.StopReason == adlcv1.StopReason_STOP_REASON_COMPLETED {
		return nil
	}
	return &exitError{code: 2}
}

// checkResumable refuses runs that Resume would refuse anyway, before tools
// (and a sandbox lookup) are involved, so the message names the real problem.
func checkResumable(ctx context.Context, store *eventlog.Store, runID string) error {
	st, err := store.State(ctx, runID)
	if err != nil {
		return err
	}
	switch st.Status {
	case eventlog.StatusFinished:
		return harness.ErrRunFinished
	case eventlog.StatusPaused:
		return harness.ErrRunPaused
	}
	return nil
}

// usesSandbox reports whether any tool command mentions the sandbox id.
func usesSandbox(a *agentconfig.Agent) bool {
	for _, t := range a.Tools {
		for _, arg := range t.Command {
			if strings.Contains(arg, sandbox.SandboxIDPlaceholder) {
				return true
			}
		}
	}
	return false
}

// attachSandbox finds the run's sandbox, checks that it still runs, and puts
// its id into the tool commands that name the placeholder. It does nothing
// for an agent without sandbox tools.
func attachSandbox(ctx context.Context, a *agentconfig.Agent, runID string,
	open func(context.Context) (sandboxLookup, error),
) error {
	if !usesSandbox(a) {
		return nil
	}
	b, err := open(ctx)
	if err != nil {
		return err
	}
	defer b.Close()
	h, err := findLiveSandbox(ctx, b, runID)
	if err != nil {
		return err
	}
	for i := range a.Tools {
		a.Tools[i].Command = sandbox.ExpandArgs(a.Tools[i].Command, h)
	}
	return nil
}

func findLiveSandbox(ctx context.Context, b sandboxLookup, runID string) (sandbox.Handle, error) {
	gone := fmt.Errorf("sandbox for run %q is not running; its workspace changes cannot be recovered. Start a new run.", runID) //nolint:staticcheck // message is specified by the brief
	found, err := b.FindByRunID(ctx, runID)
	switch {
	case err != nil:
		return sandbox.Handle{}, fmt.Errorf("resume: find sandbox: %w", err)
	case len(found) == 0:
		return sandbox.Handle{}, gone
	case len(found) > 1:
		return sandbox.Handle{}, fmt.Errorf("found %d sandboxes for run %q; remove the extra ones with docker rm", len(found), runID)
	}
	if _, err := b.Exec(ctx, found[0], []string{"true"}, sandboxProbeTimeout); err != nil {
		if ctx.Err() != nil {
			return sandbox.Handle{}, errors.Join(ctx.Err(), err)
		}
		return sandbox.Handle{}, gone
	}
	return found[0], nil
}
