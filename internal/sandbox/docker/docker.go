// Package docker is a sandbox.Sandbox backed by Docker containers.
package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// ErrUnavailable means the Docker daemon cannot be reached.
var ErrUnavailable = errors.New("docker: daemon unavailable")

// keepAlive is the container's main process. It uses sh because
// `sleep infinity` is not supported by every sleep.
const keepAlive = "while :; do sleep 3600; done"

// Backend is a sandbox.Sandbox backed by Docker containers.
type Backend struct {
	cli *client.Client

	mu    sync.Mutex
	specs map[string]sandbox.Spec // container id -> spec (needed by 1.4.2)
}

var _ sandbox.Sandbox = (*Backend)(nil)

// New connects to the daemon from the environment (DOCKER_HOST etc.) and
// pings it. If the ping fails it returns an error wrapping ErrUnavailable.
func New(ctx context.Context) (*Backend, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return &Backend{cli: cli, specs: map[string]sandbox.Spec{}}, nil
}

// Close releases the connection to the daemon.
func (b *Backend) Close() error { return b.cli.Close() }

// Create pulls the image if it is missing, then creates and starts a
// long-running container labelled with spec.RunID.
func (b *Backend) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Handle, error) {
	if err := spec.Validate(); err != nil {
		return sandbox.Handle{}, err
	}
	if err := b.ensureImage(ctx, spec.Image); err != nil {
		return sandbox.Handle{}, err
	}
	suffix, err := randomHex(4)
	if err != nil {
		return sandbox.Handle{}, fmt.Errorf("docker: random name: %w", err)
	}
	// Applied in task 1.4.2: spec.WorkspaceDir, spec.CPUs, spec.MemoryBytes
	// and spec.Network (false = no network) are not applied yet.
	cfg := &container.Config{
		Image:      spec.Image,
		Cmd:        []string{"sh", "-c", keepAlive},
		WorkingDir: sandbox.WorkspacePath,
		Env:        envList(spec.Env),
		Labels: map[string]string{
			sandbox.LabelManaged: "true",
			sandbox.LabelRunID:   spec.RunID,
		},
	}
	init := true
	hc := &container.HostConfig{Init: &init} // reaps zombie processes
	name := "adlc-" + sanitize(spec.RunID) + "-" + suffix

	created, err := b.cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hc, Name: name})
	if err != nil {
		return sandbox.Handle{}, fmt.Errorf("docker: create container: %w", err)
	}
	if _, err := b.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		b.forceRemove(created.ID)
		return sandbox.Handle{}, fmt.Errorf("docker: start container: %w", err)
	}
	b.mu.Lock()
	b.specs[created.ID] = spec
	b.mu.Unlock()
	return sandbox.Handle{ID: created.ID}, nil
}

// forceRemove removes a container even if ctx of the caller is already done.
func (b *Backend) forceRemove(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = b.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
}

func (b *Backend) ensureImage(ctx context.Context, image string) error {
	_, err := b.cli.ImageInspect(ctx, image)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("docker: inspect image %q: %w", image, err)
	}
	resp, err := b.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull image %q: %w", image, err)
	}
	if err := resp.Wait(ctx); err != nil {
		_ = resp.Close()
		return fmt.Errorf("docker: pull image %q: %w", image, err)
	}
	return resp.Close()
}

// CopyOut returns a tar archive of path inside the container.
func (b *Backend) CopyOut(ctx context.Context, h sandbox.Handle, path string) ([]byte, error) {
	res, err := b.cli.CopyFromContainer(ctx, h.ID, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil, fmt.Errorf("docker: copy out %q: %w", path, errors.Join(sandbox.ErrNotFound, err))
		}
		return nil, fmt.Errorf("docker: copy out %q: %w", path, err)
	}
	defer res.Content.Close()
	data, err := io.ReadAll(res.Content)
	if err != nil {
		return nil, fmt.Errorf("docker: read copy of %q: %w", path, err)
	}
	return data, nil
}

// Destroy force-removes the container. An unknown container gives ErrNotFound.
func (b *Backend) Destroy(ctx context.Context, h sandbox.Handle) error {
	_, err := b.cli.ContainerRemove(ctx, h.ID, client.ContainerRemoveOptions{Force: true})
	b.mu.Lock()
	delete(b.specs, h.ID)
	b.mu.Unlock()
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("docker: destroy %s: %w", shortID(h.ID), errors.Join(sandbox.ErrNotFound, err))
		}
		return fmt.Errorf("docker: destroy %s: %w", shortID(h.ID), err)
	}
	return nil
}

// FindByRunID returns the containers labelled with runID (running or not).
func (b *Backend) FindByRunID(ctx context.Context, runID string) ([]sandbox.Handle, error) {
	return b.list(ctx, client.Filters{}.Add("label", sandbox.LabelRunID+"="+runID))
}

func (b *Backend) list(ctx context.Context, f client.Filters) ([]sandbox.Handle, error) {
	res, err := b.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}
	out := make([]sandbox.Handle, 0, len(res.Items))
	for _, it := range res.Items {
		out = append(out, sandbox.Handle{ID: it.ID})
	}
	return out, nil
}

// Cleanup force-removes every container labelled with runID and returns how
// many it removed. runID "" removes every container with LabelManaged.
func (b *Backend) Cleanup(ctx context.Context, runID string) (int, error) {
	f := client.Filters{}.Add("label", sandbox.LabelManaged+"=true")
	if runID != "" {
		f = f.Add("label", sandbox.LabelRunID+"="+runID)
	}
	hs, err := b.list(ctx, f)
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, h := range hs {
		if err := b.Destroy(ctx, h); err != nil {
			if !errors.Is(err, sandbox.ErrNotFound) {
				errs = append(errs, err)
			}
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

func (b *Backend) spec(id string) (sandbox.Spec, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.specs[id]
	return s, ok
}

// envList returns env as a sorted KEY=VALUE slice.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// sanitize keeps [a-zA-Z0-9_.-] and replaces anything else with '-'.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			return r
		}
		return '-'
	}, s)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
