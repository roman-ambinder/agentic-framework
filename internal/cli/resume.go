package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
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

// sandboxLookup is the part of the sandbox backend `adlc resume` and
// `adlc approve` use: they find or restore the sandbox, then finish it like
// `adlc run`. *docker.Backend implements it.
type sandboxLookup interface {
	runBackend
	Exec(ctx context.Context, h sandbox.Handle, cmd []string, timeout time.Duration) (sandbox.ExecResult, error)
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
		Use:   "resume RUN_ID [AGENT_YAML|NAME@N|NAME@ENV]",
		Short: "Continue a run that stopped without run_completed",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			agent, err := resumeAgentPath(args[1:], o.agentPath)
			if err != nil {
				return &exitError{code: 1, err: err}
			}
			o.agentPath = agent
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return resumeAgent(ctx, o, args[0], cmd.OutOrStdout(), openSandboxBackend)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.agentPath, "agent", "", "agent file the run was started with (same as AGENT_YAML)")
	f.StringVar(&o.db, "db", defaultDBPath, "event log file")
	f.StringVar(&o.controlDB, "control-db", defaultControlDBPath, "control database (kill switches)")
	f.StringVar(&o.fakeScript, "fake-script", "", "fake model script (provider fake only)")
	f.StringVar(&o.modelName, "model-name", "", "overrides model.name from the agent file")
	addWorkspaceFlags(cmd, &o)
	return cmd
}

// resumeAgentPath returns the agent file of `adlc resume`: the positional
// AGENT_YAML or the --agent flag, which must agree when both are given.
func resumeAgentPath(positional []string, flag string) (string, error) {
	switch {
	case len(positional) == 0 && flag == "":
		return "", errors.New("resume: an agent file is required (AGENT_YAML or --agent)")
	case len(positional) == 0:
		return flag, nil
	case flag == "" || filepath.Clean(flag) == filepath.Clean(positional[0]):
		return positional[0], nil
	default:
		return "", errors.New("resume: AGENT_YAML and --agent disagree")
	}
}

// resumeAgent continues the run runID and prints the summary to stdout.
func resumeAgent(ctx context.Context, o runOptions, runID string, stdout io.Writer,
	openSandbox func(context.Context) (sandboxLookup, error),
) error {
	o, cleanupRef, err := resolveAgentRef(ctx, o)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer cleanupRef()
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
	if err := checkAgentVersion(ctx, store, runID, a); err != nil {
		return &exitError{code: 1, err: err}
	}
	ks, closeKS, err := openKillSwitch(ctx, o.controlDB)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer closeKS()
	sb, err := attachSandbox(ctx, a, runID, sandboxSource{open: openSandbox, store: store, dbDir: filepath.Dir(o.db)}, stdout)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer sb.close()
	cfg, deps, cleanup, err := buildRun(ctx, a, o, runID, store)
	if err != nil {
		sb.finishAfterError(ctx, o, store, stdout)
		return &exitError{code: 1, err: err}
	}
	defer func() { _ = cleanup() }()
	deps.KillSwitch = ks
	state, err := harness.Resume(ctx, cfg, deps)
	if err != nil {
		sb.finishAfterError(ctx, o, store, stdout)
		return &exitError{code: 1, err: err}
	}
	printSummary(stdout, state)
	if err := sb.finish(ctx, o, store, state, stdout); err != nil {
		return &exitError{code: 1, err: err}
	}
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

// sandboxSource is where attachSandbox finds a run's sandbox: the backend
// and the event log (with its directory, which snapshot paths are relative to).
type sandboxSource struct {
	open  func(context.Context) (sandboxLookup, error)
	store *eventlog.Store
	dbDir string
}

// attachSandbox gives a continued run its sandbox and puts the sandbox id
// into the tool commands. A run whose sandbox was suspended is restored from
// its latest snapshot. Otherwise, for an agent whose tools name the
// placeholder, the live container is found and probed; for one with a
// sandbox section but no placeholder, a kept container is attached if one is
// live (so finish copies it out and destroys it). It returns nil, nil when
// there is nothing to attach.
func attachSandbox(ctx context.Context, a *agentconfig.Agent, runID string, src sandboxSource, stdout io.Writer) (*runSandbox, error) {
	spec, hasSpec := a.SandboxSpec(runID)
	if !hasSpec && !usesSandbox(a) {
		return nil, nil
	}
	snap, seq, pending, err := pendingSnapshot(ctx, src.store, runID, src.dbDir)
	if err != nil {
		return nil, err
	}
	if pending && !hasSpec {
		return nil, fmt.Errorf("resume: run %q has a saved sandbox (%s) but the agent file has no sandbox section", runID, snap.TarPath)
	}
	b, err := src.open(ctx)
	if err != nil {
		if !pending && !usesSandbox(a) {
			// Only a kept container could be attached, and no tool needs it:
			// without a backend, continue without a sandbox, as before.
			fmt.Fprintf(stdout, "sandbox: could not look for a kept container: %v\n", err)
			return nil, nil
		}
		return nil, err
	}
	var h sandbox.Handle
	found := true
	switch {
	case pending:
		h, err = restoreSandbox(ctx, b, src.store, runID, spec, snap, seq, stdout)
	case usesSandbox(a):
		h, err = findLiveSandbox(ctx, b, runID)
	default:
		h, found, err = findKeptSandbox(ctx, b, runID)
	}
	if err != nil || !found {
		_ = b.Close()
		return nil, err
	}
	expandSandboxID(a, h)
	return &runSandbox{backend: b, handle: h, runID: runID, suspends: hasSpec && a.Sandbox.Suspends(), spec: spec}, nil
}

// errSandboxGone matches the error findLiveSandbox returns when the run has
// no live container.
var errSandboxGone = errors.New("cli: sandbox is not running")

// sandboxGoneError is findLiveSandbox's "no live container" error; its text
// is the one users have always seen.
type sandboxGoneError struct{ runID string }

func (e sandboxGoneError) Error() string {
	return fmt.Sprintf("sandbox for run %q is not running; its workspace changes cannot be recovered. Start a new run.", e.runID)
}

// Is makes errors.Is(err, errSandboxGone) true.
func (e sandboxGoneError) Is(target error) bool { return target == errSandboxGone }

// findKeptSandbox looks for the live container of an agent whose tools do
// not name the sandbox (so it was only kept, e.g. with `pause: keep`). None,
// or one that does not answer, is not an error: the run continues without a
// sandbox, as before. More than one is.
func findKeptSandbox(ctx context.Context, b sandboxLookup, runID string) (sandbox.Handle, bool, error) {
	h, err := findLiveSandbox(ctx, b, runID)
	if err == nil {
		return h, true, nil
	}
	if errors.Is(err, errSandboxGone) {
		return sandbox.Handle{}, false, nil
	}
	return sandbox.Handle{}, false, err
}

func findLiveSandbox(ctx context.Context, b sandboxLookup, runID string) (sandbox.Handle, error) {
	gone := sandboxGoneError{runID: runID}
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

// checkAgentVersion refuses with harness.ErrAgentMismatch when the agent file
// a is not the version that started runID, before any sandbox is restored.
func checkAgentVersion(ctx context.Context, store *eventlog.Store, runID string, a *agentconfig.Agent) error {
	st, err := store.State(ctx, runID)
	if err != nil {
		return err
	}
	if st.AgentVersion != a.AgentID() {
		return fmt.Errorf("%w: run has %s, agent file is %s", harness.ErrAgentMismatch, st.AgentVersion, a.AgentID())
	}
	return nil
}
