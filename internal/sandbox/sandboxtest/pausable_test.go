package sandboxtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

func newSpec() sandbox.Spec { return sandbox.Spec{RunID: "run-1", Image: "img:1"} }

func TestPausableSuspendWritesTarAndDestroys(t *testing.T) {
	ctx := context.Background()
	p := &Pausable{Workspace: []byte("tar-bytes")}
	h, err := p.Create(ctx, newSpec())
	if err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(t.TempDir(), "ws.tar")
	snap, err := p.Suspend(ctx, h, tarPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(tarPath)
	if err != nil || string(got) != "tar-bytes" {
		t.Fatalf("tar content = %q, err %v", got, err)
	}
	sum := sha256.Sum256([]byte("tar-bytes"))
	want := sandbox.Snapshot{
		TarPath:          tarPath,
		SHA256:           "sha256:" + hex.EncodeToString(sum[:]),
		Bytes:            9,
		ImageID:          "sha256:fake-img:1",
		ProcessesSkipped: sandbox.ProcessesNotSupported,
	}
	if snap != want {
		t.Fatalf("snapshot = %+v, want %+v", snap, want)
	}
	if live := p.Live(); len(live) != 0 {
		t.Fatalf("Live = %v, want empty", live)
	}
}

func TestPausableRestoreRejectsChangedTar(t *testing.T) {
	ctx := context.Background()
	p := &Pausable{Workspace: []byte("a")}
	h, _ := p.Create(ctx, newSpec())
	tarPath := filepath.Join(t.TempDir(), "ws.tar")
	snap, err := p.Suspend(ctx, h, tarPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tarPath, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = p.Restore(ctx, newSpec(), snap)
	if !errors.Is(err, sandbox.ErrSnapshotChanged) {
		t.Fatalf("err = %v, want ErrSnapshotChanged", err)
	}
	if live := p.Live(); len(live) != 0 {
		t.Fatalf("Live = %v, want empty", live)
	}
}

func TestPausableRestoreRejectsMissingImage(t *testing.T) {
	ctx := context.Background()
	p := &Pausable{Workspace: []byte("a"), Images: map[string]bool{}}
	h, _ := p.Create(ctx, newSpec())
	snap, err := p.Suspend(ctx, h, filepath.Join(t.TempDir(), "ws.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Restore(ctx, newSpec(), snap); !errors.Is(err, sandbox.ErrImageGone) {
		t.Fatalf("err = %v, want ErrImageGone", err)
	}
	p.Images[snap.ImageID] = true
	h2, err := p.Restore(ctx, newSpec(), snap)
	if err != nil || len(p.Live()) != 1 || p.Live()[0] != h2 {
		t.Fatalf("restore with image: %v, live %v", err, p.Live())
	}
}

func TestPausableForcedErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	p := &Pausable{SuspendErr: boom, RestoreErr: boom}
	h, _ := p.Create(ctx, newSpec())
	if _, err := p.Suspend(ctx, h, filepath.Join(t.TempDir(), "x")); !errors.Is(err, boom) {
		t.Fatalf("Suspend err = %v", err)
	}
	if len(p.Live()) != 1 {
		t.Fatal("failed Suspend must keep the handle live")
	}
	if _, err := p.Restore(ctx, newSpec(), sandbox.Snapshot{}); !errors.Is(err, boom) {
		t.Fatalf("Restore err = %v", err)
	}
}

func TestPausableFindByRunID(t *testing.T) {
	ctx := context.Background()
	p := &Pausable{}
	a, _ := p.Create(ctx, newSpec())
	b, _ := p.Create(ctx, newSpec())
	_, _ = p.Create(ctx, sandbox.Spec{RunID: "other", Image: "img:1"})
	got, err := p.FindByRunID(ctx, "run-1")
	if err != nil || len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("FindByRunID = %v, %v", got, err)
	}
	if err := p.Destroy(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _ = p.FindByRunID(ctx, "run-1")
	if len(got) != 1 || got[0] != b {
		t.Fatalf("after destroy: %v", got)
	}
	if _, err := p.Exec(ctx, a, []string{"true"}, time.Second); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("Exec on destroyed = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPausableImageID(t *testing.T) {
	ctx := context.Background()
	forced := errors.New("boom")
	tests := []struct {
		name    string
		destroy bool
		forceE  error
		want    string
		wantErr error
	}{
		{"live handle", false, nil, "sha256:fake-img:1", nil},
		{"destroyed handle", true, nil, "", sandbox.ErrNotFound},
		{"forced error", false, forced, "", forced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pausable{ImageIDErr: tt.forceE}
			h, err := p.Create(ctx, newSpec())
			if err != nil {
				t.Fatal(err)
			}
			if tt.destroy {
				if err := p.Destroy(ctx, h); err != nil {
					t.Fatal(err)
				}
			}
			got, err := p.ImageID(ctx, h)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("ImageID = %q, want %q", got, tt.want)
			}
		})
	}
}
