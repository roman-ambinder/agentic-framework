//go:build docker

package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

func suspendedSandbox(t *testing.T) (*Backend, sandbox.Spec, sandbox.Snapshot) {
	t.Helper()
	b, h := isoSandbox(t, sandbox.Spec{})
	spec, ok := b.spec(h.ID)
	if !ok {
		t.Fatal("spec not recorded")
	}
	isoExec(t, b, h, "sh", "-c", `echo hi > /workspace/a.txt && printf "#!/bin/sh\n" > /workspace/run.sh && chmod 755 /workspace/run.sh && ln -s a.txt /workspace/link`)
	snap, err := b.Suspend(context.Background(), h, filepath.Join(t.TempDir(), "1.tar"))
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	return b, spec, snap
}

func assertNoContainer(t *testing.T, b *Backend, runID string) {
	t.Helper()
	hs, err := b.FindByRunID(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 0 {
		t.Fatalf("FindByRunID = %v, want none", hs)
	}
}

func TestSuspendRestoreRoundTrip(t *testing.T) {
	b, spec, snap := suspendedSandbox(t)
	if snap.ProcessesSkipped != sandbox.ProcessesNotSupported || snap.ImageID == "" || snap.Bytes <= 0 {
		t.Fatalf("bad snapshot %+v", snap)
	}
	assertNoContainer(t, b, spec.RunID)
	h, err := b.Restore(context.Background(), spec, snap)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	res := isoExec(t, b, h, "sh", "-c", `cat /workspace/link; stat -c "%a %u" /workspace/run.sh; readlink /workspace/link`)
	if want := "hi\n755 65532\na.txt\n"; string(res.Stdout) != want {
		t.Fatalf("Stdout = %q, want %q (stderr %q)", res.Stdout, want, res.Stderr)
	}
}

func TestRestoreRejectsChangedTar(t *testing.T) {
	b, spec, snap := suspendedSandbox(t)
	f, err := os.OpenFile(snap.TarPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	_, err = b.Restore(context.Background(), spec, snap)
	if !errors.Is(err, sandbox.ErrSnapshotChanged) {
		t.Fatalf("err = %v, want ErrSnapshotChanged", err)
	}
	assertNoContainer(t, b, spec.RunID)
}

func TestRestoreRejectsMissingImage(t *testing.T) {
	b, spec, snap := suspendedSandbox(t)
	snap.ImageID = "sha256:" + strings.Repeat("0", 64)
	_, err := b.Restore(context.Background(), spec, snap)
	if !errors.Is(err, sandbox.ErrImageGone) {
		t.Fatalf("err = %v, want ErrImageGone", err)
	}
}

func TestSuspendKeepsContainerWhenTarPathUnwritable(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{})
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Suspend(context.Background(), h, filepath.Join(blocker, "1.tar")); err == nil {
		t.Fatal("Suspend succeeded, want error")
	}
	if res := isoExec(t, b, h, "true"); res.ExitCode != 0 {
		t.Fatalf("container not usable: %+v", res)
	}
}
