//go:build docker

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

func testImage() string {
	if img := os.Getenv("ADLC_SANDBOX_TEST_IMAGE"); img != "" {
		return img
	}
	return "busybox:1.37"
}

func uniqueRunID(t *testing.T) string {
	t.Helper()
	suffix, err := randomHex(4)
	if err != nil {
		t.Fatal(err)
	}
	return sanitize("test-" + t.Name() + "-" + suffix)
}

func newBackend(t *testing.T, runID string) *Backend {
	t.Helper()
	b, err := New(context.Background())
	if errors.Is(err, ErrUnavailable) {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_, _ = b.Cleanup(context.Background(), runID)
		_ = b.Close()
	})
	return b
}

func TestDockerConformance(t *testing.T) {
	runID := uniqueRunID(t)
	b := newBackend(t, runID)
	sandboxtest.RunConformance(t, b, func() sandbox.Spec {
		return sandbox.Spec{RunID: runID, Image: testImage()}
	})
}

func TestDockerLabelsAndFind(t *testing.T) {
	ctx := context.Background()
	runID := uniqueRunID(t)
	b := newBackend(t, runID)
	h, err := b.Create(ctx, sandbox.Spec{RunID: runID, Image: testImage()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.FindByRunID(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != h {
		t.Fatalf("FindByRunID = %v, want [%v]", got, h)
	}
}

func TestDockerCleanup(t *testing.T) {
	ctx := context.Background()
	runID := uniqueRunID(t)
	b := newBackend(t, runID)
	for range 2 {
		if _, err := b.Create(ctx, sandbox.Spec{RunID: runID, Image: testImage()}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := b.Cleanup(ctx, runID)
	if err != nil || n != 2 {
		t.Fatalf("Cleanup = %d, %v; want 2, nil", n, err)
	}
	got, err := b.FindByRunID(ctx, runID)
	if err != nil || len(got) != 0 {
		t.Fatalf("FindByRunID after Cleanup = %v, %v", got, err)
	}
}

func TestDockerCopyOut(t *testing.T) {
	ctx := context.Background()
	runID := uniqueRunID(t)
	b := newBackend(t, runID)
	h, err := b.Create(ctx, sandbox.Spec{RunID: runID, Image: testImage()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(ctx, h, []string{"sh", "-c", "echo hi > b.txt"}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	data, err := b.CopyOut(ctx, h, sandbox.WorkspacePath+"/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	hdr, err := tr.Next()
	if err != nil || hdr.Name != "b.txt" {
		t.Fatalf("first entry = %v, %v", hdr, err)
	}
	body, _ := io.ReadAll(tr)
	if string(body) != "hi\n" {
		t.Fatalf("content = %q", body)
	}
}

func TestDockerCreateUnknownImageFails(t *testing.T) {
	ctx := context.Background()
	runID := uniqueRunID(t)
	b := newBackend(t, runID)
	_, err := b.Create(ctx, sandbox.Spec{RunID: runID, Image: "adlc-test-does-not-exist:never"})
	if err == nil {
		t.Fatal("Create with unknown image succeeded")
	}
	got, err := b.FindByRunID(ctx, runID)
	if err != nil || len(got) != 0 {
		t.Fatalf("FindByRunID = %v, %v; want none", got, err)
	}
}
