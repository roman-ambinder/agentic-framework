//go:build docker

package docker

import (
	"context"
	"testing"

	"github.com/moby/moby/client"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

func TestImageIDOfLocallyTaggedImage(t *testing.T) {
	ctx := context.Background()
	const ref = "adlc-test-local:1"
	runID := uniqueRunID(t)
	b := newBackend(t, runID)
	if _, err := b.cli.ImageInspect(ctx, testImage()); err != nil {
		t.Skipf("test image %s not present: %v", testImage(), err)
	}
	if _, err := b.cli.ImageTag(ctx, client.ImageTagOptions{Source: testImage(), Target: ref}); err != nil {
		t.Fatalf("ImageTag: %v", err)
	}
	t.Cleanup(func() {
		_, _ = b.cli.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{})
	})
	h, err := b.Create(ctx, sandbox.Spec{RunID: runID, Image: ref})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := b.ImageID(ctx, h)
	if err != nil {
		t.Fatalf("ImageID: %v", err)
	}
	insp, err := b.cli.ImageInspect(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != insp.ID {
		t.Fatalf("ImageID = %q, want %q", got, insp.ID)
	}
}
