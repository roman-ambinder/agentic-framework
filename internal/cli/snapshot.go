package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// shortIDLen is how many characters of a sandbox id the CLI prints.
const shortIDLen = 12

// snapshotPath is where the workspace tar of runID's pause at seq is written:
// .adlc/snapshots/<run_id>/<seq>.tar next to the event log.
func snapshotPath(dbPath, runID string, seq uint64) string {
	return filepath.Join(filepath.Dir(dbPath), "snapshots", runID, fmt.Sprintf("%d.tar", seq))
}

// pendingSnapshot returns the run's latest sandbox_suspended if no later
// sandbox_restored names it. SnapshotRef is relative to dbDir; the returned
// TarPath is absolute. The fold keeps no such field (it would change every
// digest), so the events are scanned here.
func pendingSnapshot(ctx context.Context, store *eventlog.Store, runID, dbDir string) (sandbox.Snapshot, uint64, bool, error) {
	events, err := store.Read(ctx, runID, 0)
	if err != nil {
		return sandbox.Snapshot{}, 0, false, fmt.Errorf("cli: read sandbox events: %w", err)
	}
	var last *adlcv1.SandboxSuspended
	var seq uint64
	for _, ev := range events {
		if s := ev.GetSandboxSuspended(); s != nil {
			last, seq = s, ev.GetSeq()
		}
		if r := ev.GetSandboxRestored(); r != nil && last != nil && r.GetSuspendedSeq() == seq {
			last = nil
		}
	}
	if last == nil {
		return sandbox.Snapshot{}, 0, false, nil
	}
	tar, err := filepath.Abs(filepath.Join(dbDir, last.GetSnapshotRef()))
	if err != nil {
		return sandbox.Snapshot{}, 0, false, fmt.Errorf("cli: snapshot path: %w", err)
	}
	return sandbox.Snapshot{
		TarPath:          tar,
		SHA256:           last.GetWorkspaceSha256(),
		Bytes:            last.GetWorkspaceBytes(),
		ImageID:          last.GetImageId(),
		ProcessesSkipped: last.GetProcessesSkipped(),
	}, seq, true, nil
}

// appendSuspended records snap; its SnapshotRef is TarPath relative to dbDir.
func appendSuspended(ctx context.Context, store *eventlog.Store, runID string, snap sandbox.Snapshot, dbDir string) error {
	ref, err := relativeTo(dbDir, snap.TarPath)
	if err != nil {
		return err
	}
	_, err = store.Append(ctx, &adlcv1.Event{RunId: runID, Payload: &adlcv1.Event_SandboxSuspended{
		SandboxSuspended: &adlcv1.SandboxSuspended{
			SnapshotRef:      ref,
			WorkspaceSha256:  snap.SHA256,
			WorkspaceBytes:   snap.Bytes,
			ImageId:          snap.ImageID,
			ProcessesSkipped: snap.ProcessesSkipped,
		},
	}})
	if err != nil {
		return fmt.Errorf("cli: record sandbox_suspended: %w", err)
	}
	return nil
}

// relativeTo returns path relative to dir, both made absolute first.
func relativeTo(dir, path string) (string, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("cli: snapshot path: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("cli: snapshot path: %w", err)
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return "", fmt.Errorf("cli: snapshot path: %w", err)
	}
	return rel, nil
}

// appendRestored records that h was restored from the sandbox_suspended at
// suspendedSeq. Processes are never restored yet.
func appendRestored(ctx context.Context, store *eventlog.Store, runID string, suspendedSeq uint64, snap sandbox.Snapshot, h sandbox.Handle) error {
	_, err := store.Append(ctx, &adlcv1.Event{RunId: runID, Payload: &adlcv1.Event_SandboxRestored{
		SandboxRestored: &adlcv1.SandboxRestored{
			SuspendedSeq:  suspendedSeq,
			Processes:     false,
			FallbackCause: snap.ProcessesSkipped,
			SandboxId:     h.ID,
		},
	}})
	if err != nil {
		return fmt.Errorf("cli: record sandbox_restored: %w", err)
	}
	return nil
}

// suspend saves /workspace of a run that waits for approval and records it.
// If Suspend fails the container is kept and nothing is appended. If the
// record fails after Suspend, the sandbox is restored from the tar
// (restoreUnrecorded).
func (s *runSandbox) suspend(ctx context.Context, o runOptions, store *eventlog.Store, state *eventlog.RunState, stdout io.Writer) error {
	bg := context.WithoutCancel(ctx)
	tar := snapshotPath(o.db, s.runID, state.LastSeq+1)
	snap, err := s.suspendTo(bg, tar)
	if err != nil {
		fmt.Fprintf(stdout, "sandbox: pause failed: %v\n", err)
		s.keepForResume(stdout)
		return nil
	}
	if err := appendSuspended(bg, store, s.runID, snap, filepath.Dir(o.db)); err != nil {
		return s.restoreUnrecorded(bg, snap, err, stdout)
	}
	if snap.ProcessesSkipped == "" {
		fmt.Fprint(stdout, "sandbox: paused (files + processes)\n")
	} else {
		fmt.Fprintf(stdout, "sandbox: paused (files only: %s)\n", snap.ProcessesSkipped)
	}
	return nil
}

// restoreUnrecorded brings the sandbox back from snap after its pause could
// not be recorded (recordErr), so the run keeps a live container as when
// Suspend fails. If that fails too, the error names the tar and how to
// extract it: no event points at it.
func (s *runSandbox) restoreUnrecorded(ctx context.Context, snap sandbox.Snapshot, recordErr error, stdout io.Writer) error {
	h, err := s.backend.Restore(ctx, s.spec, snap)
	if err != nil {
		return fmt.Errorf("run: sandbox saved to %s but not recorded (%v) and not restored: %w; recover the files with: tar -xf %s",
			snap.TarPath, recordErr, err, snap.TarPath)
	}
	s.handle = h
	fmt.Fprintf(stdout, "sandbox: pause not recorded (%v); restored %s and kept it for resume\n", recordErr, shortSandboxID(h.ID))
	return nil
}

// suspendTo creates the tar's directory and calls Suspend.
func (s *runSandbox) suspendTo(ctx context.Context, tar string) (sandbox.Snapshot, error) {
	if err := os.MkdirAll(filepath.Dir(tar), 0o700); err != nil {
		return sandbox.Snapshot{}, fmt.Errorf("run: create snapshot directory: %w", err)
	}
	return s.backend.Suspend(ctx, s.handle, tar)
}

// restoreSandbox recreates the sandbox of runID from snap and records it.
// On any error no sandbox is left running and nothing is appended.
func restoreSandbox(ctx context.Context, b runBackend, store *eventlog.Store, runID string, spec sandbox.Spec,
	snap sandbox.Snapshot, seq uint64, stdout io.Writer,
) (sandbox.Handle, error) {
	h, err := b.Restore(ctx, spec, snap)
	if err != nil {
		return sandbox.Handle{}, fmt.Errorf("resume: restore sandbox from %s: %w", snap.TarPath, err)
	}
	if err := appendRestored(ctx, store, runID, seq, snap, h); err != nil {
		_ = b.Destroy(context.WithoutCancel(ctx), h)
		return sandbox.Handle{}, fmt.Errorf("resume: %w", err)
	}
	fmt.Fprintf(stdout, "sandbox: restored %s (files only: /workspace kept; running processes and files outside /workspace were not kept)\n", shortSandboxID(h.ID))
	return h, nil
}

// shortSandboxID returns the first shortIDLen characters of id.
func shortSandboxID(id string) string {
	if len(id) > shortIDLen {
		return id[:shortIDLen]
	}
	return id
}

// finishAfterError handles the sandbox when continuing a run failed: a run
// that is still paused is paused again, any other run keeps its container.
func (s *runSandbox) finishAfterError(ctx context.Context, o runOptions, store *eventlog.Store, stdout io.Writer) {
	if s == nil {
		return
	}
	st, err := store.State(context.WithoutCancel(ctx), s.runID)
	if err != nil || st.Status != eventlog.StatusPaused {
		s.keepForResume(stdout)
		return
	}
	if err := s.finish(ctx, o, store, st, stdout); err != nil {
		fmt.Fprintf(stdout, "sandbox: %v\n", err)
	}
}
