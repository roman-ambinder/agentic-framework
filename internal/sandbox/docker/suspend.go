package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

var _ sandbox.Suspender = (*Backend)(nil)

// Suspend writes the /workspace archive of h to tarPath, destroys the
// container and returns the snapshot. On any error before the container is
// destroyed it is left untouched and no file remains at tarPath.
func (b *Backend) Suspend(ctx context.Context, h sandbox.Handle, tarPath string) (sandbox.Snapshot, error) {
	data, err := b.CopyOut(ctx, h, sandbox.WorkspacePath)
	if err != nil {
		return sandbox.Snapshot{}, err
	}
	if err := writeFileAtomic(tarPath, data); err != nil {
		return sandbox.Snapshot{}, err
	}
	snap, err := b.snapshotOf(ctx, h, tarPath)
	if err != nil {
		_ = os.Remove(tarPath)
		return sandbox.Snapshot{}, err
	}
	if err := b.Destroy(ctx, h); err != nil {
		_ = os.Remove(tarPath)
		return sandbox.Snapshot{}, err
	}
	return snap, nil
}

func (b *Backend) snapshotOf(ctx context.Context, h sandbox.Handle, tarPath string) (sandbox.Snapshot, error) {
	sum, n, err := fileSHA256(tarPath)
	if err != nil {
		return sandbox.Snapshot{}, err
	}
	res, err := b.cli.ContainerInspect(ctx, h.ID, client.ContainerInspectOptions{})
	if err != nil {
		return sandbox.Snapshot{}, fmt.Errorf("docker: suspend %s: inspect: %w", shortID(h.ID), err)
	}
	return sandbox.Snapshot{
		TarPath:          tarPath,
		SHA256:           sum,
		Bytes:            n,
		ImageID:          res.Container.Image,
		ProcessesSkipped: sandbox.ProcessesNotSupported,
	}, nil
}

// Restore creates a container for spec from the image recorded in snap and
// fills /workspace from the archive. It returns ErrSnapshotChanged when the
// archive no longer matches snap.SHA256 and ErrImageGone when the image is
// missing.
func (b *Backend) Restore(ctx context.Context, spec sandbox.Spec, snap sandbox.Snapshot) (sandbox.Handle, error) {
	sum, _, err := fileSHA256(snap.TarPath)
	if err != nil {
		return sandbox.Handle{}, err
	}
	if sum != snap.SHA256 {
		return sandbox.Handle{}, fmt.Errorf("docker: restore %s: %w", snap.TarPath, sandbox.ErrSnapshotChanged)
	}
	if _, err := b.cli.ImageInspect(ctx, snap.ImageID); err != nil {
		if cerrdefs.IsNotFound(err) {
			return sandbox.Handle{}, fmt.Errorf("docker: restore: image %s: %w", snap.ImageID, sandbox.ErrImageGone)
		}
		return sandbox.Handle{}, fmt.Errorf("docker: restore: inspect image %s: %w", snap.ImageID, err)
	}
	spec.Image = snap.ImageID
	spec.WorkspaceDir = ""
	h, err := b.Create(ctx, spec)
	if err != nil {
		return sandbox.Handle{}, err
	}
	if err := b.copyTarInto(ctx, h, snap.TarPath); err != nil {
		b.forceRemove(h.ID)
		return sandbox.Handle{}, err
	}
	return h, nil
}

// copyTarInto extracts the tar file at path under / in the container,
// keeping the owners recorded in the archive.
func (b *Backend) copyTarInto(ctx context.Context, h sandbox.Handle, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("docker: restore: open %s: %w", path, err)
	}
	defer f.Close()
	_, err = b.cli.CopyToContainer(ctx, h.ID, client.CopyToContainerOptions{
		DestinationPath: "/",
		Content:         f,
		CopyUIDGID:      true,
	})
	if err != nil {
		return fmt.Errorf("docker: restore: copy workspace: %w", err)
	}
	return nil
}

// writeFileAtomic writes data to path through a temp file in the same
// directory and a rename, creating parent directories. On error nothing is
// left at path.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("docker: suspend: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".suspend-*.tmp")
	if err != nil {
		return fmt.Errorf("docker: suspend: temp file: %w", err)
	}
	_, werr := io.Copy(tmp, bytes.NewReader(data))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("docker: suspend: write %s: %w", tmp.Name(), err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("docker: suspend: chmod: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("docker: suspend: rename to %s: %w", path, err)
	}
	return nil
}

// fileSHA256 returns "sha256:<hex>" and the size in bytes of the file at path.
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("docker: hash %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("docker: hash %s: %w", path, err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}
