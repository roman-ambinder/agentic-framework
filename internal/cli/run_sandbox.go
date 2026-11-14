package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/evals"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/docker"
)

// runBackend is the part of the sandbox backend `adlc run` uses;
// *docker.Backend implements it.
type runBackend interface {
	sandbox.Suspender
	Create(ctx context.Context, spec sandbox.Spec) (sandbox.Handle, error)
	CopyOut(ctx context.Context, h sandbox.Handle, path string) ([]byte, error)
	Destroy(ctx context.Context, h sandbox.Handle) error
	FindByRunID(ctx context.Context, runID string) ([]sandbox.Handle, error)
	Close() error
}

// openRunBackend connects to a sandbox backend.
type openRunBackend func(ctx context.Context) (runBackend, error)

// openDockerRunBackend connects to the Docker backend.
func openDockerRunBackend(ctx context.Context) (runBackend, error) {
	b, err := docker.New(ctx)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// runSandbox is the sandbox of one `adlc run`. A nil *runSandbox means the
// agent has none; every method then does nothing.
type runSandbox struct {
	backend runBackend
	handle  sandbox.Handle
	runID   string
	// suspends is false for `pause: keep`: the container stays while paused.
	suspends bool
	// spec is what the sandbox was created (or restored) from; a failed
	// pause record restores from it.
	spec sandbox.Spec
	// image is the image the sandbox runs, recorded in run_started.
	image *adlcv1.SandboxImage
}

// startRunSandbox creates the sandbox the agent declares and puts its id into
// the tool commands. It returns nil, nil for an agent without a sandbox.
func startRunSandbox(ctx context.Context, a *agentconfig.Agent, runID string, open openRunBackend, stdout io.Writer) (*runSandbox, error) {
	spec, ok := a.SandboxSpec(runID)
	if !ok {
		return nil, nil
	}
	b, err := open(ctx)
	if err != nil {
		return nil, fmt.Errorf("run: docker is not available: %w", err)
	}
	h, err := b.Create(ctx, spec)
	if err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("run: create sandbox: %w", err)
	}
	expandSandboxID(a, h)
	image := resolveSandboxImage(ctx, b, h, spec.Image, stdout)
	return &runSandbox{backend: b, handle: h, runID: runID, suspends: a.Sandbox.Suspends(), spec: spec, image: image}, nil
}

// resolveSandboxImage reads the content ID of the image the sandbox runs. A
// backend that cannot, or a failed read, leaves Id empty: it never fails the run.
func resolveSandboxImage(ctx context.Context, b runBackend, h sandbox.Handle, ref string, stdout io.Writer) *adlcv1.SandboxImage {
	img := &adlcv1.SandboxImage{Ref: ref}
	r, ok := b.(sandbox.ImageResolver)
	if !ok {
		return img
	}
	id, err := r.ImageID(ctx, h)
	if err != nil {
		fmt.Fprintf(stdout, "sandbox: could not read the image ID: %v\n", err)
		return img
	}
	img.Id = id
	fmt.Fprintf(stdout, "sandbox: image %s (%s)\n", ref, sandbox.ShortImageID(id))
	return img
}

// imageOrNil returns the recorded image; a nil *runSandbox has none.
func (s *runSandbox) imageOrNil() *adlcv1.SandboxImage {
	if s == nil {
		return nil
	}
	return s.image
}

// expandSandboxID replaces {{sandbox_id}} in every tool command.
func expandSandboxID(a *agentconfig.Agent, h sandbox.Handle) {
	for i := range a.Tools {
		a.Tools[i].Command = sandbox.ExpandArgs(a.Tools[i].Command, h)
	}
}

// close releases the backend connection; the container is not touched.
func (s *runSandbox) close() {
	if s != nil {
		_ = s.backend.Close()
	}
}

// keepForResume tells the user the container stays for `adlc resume`.
func (s *runSandbox) keepForResume(stdout io.Writer) {
	if s != nil {
		fmt.Fprintf(stdout, "sandbox: kept %s for resume\n", s.handle.ID)
	}
}

// destroy removes the container even when ctx is cancelled.
func (s *runSandbox) destroy(ctx context.Context, stdout io.Writer) {
	if s == nil {
		return
	}
	if err := s.backend.Destroy(context.WithoutCancel(ctx), s.handle); err != nil {
		fmt.Fprintf(stdout, "sandbox: destroy %s failed: %v\n", s.handle.ID, err)
	}
}

// finish handles the sandbox after a run that returned a state. A run that
// awaits approval has its sandbox suspended to a snapshot next to the event
// log o.db (or kept, with `pause: keep` or when Suspend fails). Otherwise
// /workspace is copied out, the run's snapshot tars are deleted, and the
// container destroyed unless --keep-sandbox
// is set. If the copy fails the container is kept so the work is not lost.
func (s *runSandbox) finish(ctx context.Context, o runOptions, store *eventlog.Store, state *eventlog.RunState, stdout io.Writer) error {
	if s == nil {
		return nil
	}
	if state.StopReason == adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL {
		if s.suspends {
			return s.suspend(ctx, o, store, state, stdout)
		}
		s.keepForResume(stdout)
		return nil
	}
	out := o.workspaceOut
	if out == "" {
		out = filepath.Join(".adlc", "workspaces", s.runID)
	}
	bg := context.WithoutCancel(ctx)
	data, err := s.backend.CopyOut(bg, s.handle, sandbox.WorkspacePath)
	if err == nil {
		err = evals.ExtractWorkspaceTar(data, out)
	}
	if err != nil {
		fmt.Fprintf(stdout, "sandbox: kept %s (workspace copy failed)\n", s.handle.ID)
		return fmt.Errorf("run: copy workspace out: %w", err)
	}
	fmt.Fprintf(stdout, "workspace: %s\n", out)
	if err := removeSnapshots(o.db, s.runID); err != nil {
		fmt.Fprintf(stdout, "sandbox: could not remove snapshots: %v\n", err)
	}
	if o.keepSandbox {
		fmt.Fprintf(stdout, "sandbox: kept %s\n", s.handle.ID)
		return nil
	}
	s.destroy(ctx, stdout)
	return nil
}
