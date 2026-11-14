package docker

import (
	"context"
	"errors"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

var _ sandbox.ImageResolver = (*Backend)(nil)

// ImageID returns the ID of the image container h really uses, even if the
// tag it was created from has moved since. An unknown container gives
// ErrNotFound.
func (b *Backend) ImageID(ctx context.Context, h sandbox.Handle) (string, error) {
	res, err := b.cli.ContainerInspect(ctx, h.ID, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return "", fmt.Errorf("docker: image id of %s: %w", shortID(h.ID), errors.Join(sandbox.ErrNotFound, err))
		}
		return "", fmt.Errorf("docker: image id of %s: %w", shortID(h.ID), err)
	}
	return res.Container.Image, nil
}
