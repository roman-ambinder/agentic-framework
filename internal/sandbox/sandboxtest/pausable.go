package sandboxtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// Pausable is an in-memory Sandbox and Suspender for tests. Create returns
// Handle{ID: "fake-<n>"}; Suspend writes Workspace to the tar path.
type Pausable struct {
	// Workspace is what Suspend writes to the tar file.
	Workspace []byte
	// Images lists the image IDs Restore accepts; nil accepts every image.
	Images map[string]bool
	// SuspendErr and RestoreErr, when set, make Suspend and Restore fail.
	SuspendErr, RestoreErr error

	mu      sync.Mutex
	next    int
	order   []string
	boxes   map[string]sandbox.Spec
	removed map[string]bool
}

var (
	_ sandbox.Sandbox   = (*Pausable)(nil)
	_ sandbox.Suspender = (*Pausable)(nil)
)

// Create stores spec and returns a new handle.
func (p *Pausable) Create(_ context.Context, spec sandbox.Spec) (sandbox.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.createLocked(spec), nil
}

func (p *Pausable) createLocked(spec sandbox.Spec) sandbox.Handle {
	if p.boxes == nil {
		p.boxes = map[string]sandbox.Spec{}
		p.removed = map[string]bool{}
	}
	p.next++
	id := fmt.Sprintf("fake-%d", p.next)
	p.boxes[id] = spec
	p.order = append(p.order, id)
	return sandbox.Handle{ID: id}
}

func (p *Pausable) liveLocked(h sandbox.Handle) (sandbox.Spec, error) {
	spec, ok := p.boxes[h.ID]
	if !ok || p.removed[h.ID] {
		return sandbox.Spec{}, fmt.Errorf("sandboxtest: %s: %w", h.ID, sandbox.ErrNotFound)
	}
	return spec, nil
}

// Exec returns a zero result with exit code 0 for a live handle.
func (p *Pausable) Exec(_ context.Context, h sandbox.Handle, _ []string, _ time.Duration) (sandbox.ExecResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.liveLocked(h)
	return sandbox.ExecResult{}, err
}

// CopyOut returns the bytes in Workspace for a live handle.
func (p *Pausable) CopyOut(_ context.Context, h sandbox.Handle, _ string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.liveLocked(h); err != nil {
		return nil, err
	}
	return append([]byte(nil), p.Workspace...), nil
}

// Destroy marks h destroyed; an unknown handle is ErrNotFound.
func (p *Pausable) Destroy(_ context.Context, h sandbox.Handle) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.boxes[h.ID]; !ok {
		return fmt.Errorf("sandboxtest: %s: %w", h.ID, sandbox.ErrNotFound)
	}
	p.removed[h.ID] = true
	return nil
}

// Suspend writes Workspace to tarPath, destroys h and describes the file.
func (p *Pausable) Suspend(_ context.Context, h sandbox.Handle, tarPath string) (sandbox.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.SuspendErr != nil {
		return sandbox.Snapshot{}, p.SuspendErr
	}
	spec, err := p.liveLocked(h)
	if err != nil {
		return sandbox.Snapshot{}, err
	}
	if err := os.WriteFile(tarPath, p.Workspace, 0o600); err != nil {
		return sandbox.Snapshot{}, fmt.Errorf("sandboxtest: write tar: %w", err)
	}
	p.removed[h.ID] = true
	return sandbox.Snapshot{
		TarPath:          tarPath,
		SHA256:           hashHex(p.Workspace),
		Bytes:            int64(len(p.Workspace)),
		ImageID:          "sha256:fake-" + spec.Image,
		ProcessesSkipped: sandbox.ProcessesNotSupported,
	}, nil
}

// Restore checks snap, then creates a sandbox like Create.
func (p *Pausable) Restore(_ context.Context, spec sandbox.Spec, snap sandbox.Snapshot) (sandbox.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.RestoreErr != nil {
		return sandbox.Handle{}, p.RestoreErr
	}
	data, err := os.ReadFile(snap.TarPath)
	if err != nil {
		return sandbox.Handle{}, fmt.Errorf("sandboxtest: read tar: %w", err)
	}
	if hashHex(data) != snap.SHA256 {
		return sandbox.Handle{}, fmt.Errorf("sandboxtest: %s: %w", snap.TarPath, sandbox.ErrSnapshotChanged)
	}
	if p.Images != nil && !p.Images[snap.ImageID] {
		return sandbox.Handle{}, fmt.Errorf("sandboxtest: %s: %w", snap.ImageID, sandbox.ErrImageGone)
	}
	return p.createLocked(spec), nil
}

// Live returns the handles that are not destroyed, in creation order.
func (p *Pausable) Live() []sandbox.Handle {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []sandbox.Handle
	for _, id := range p.order {
		if !p.removed[id] {
			out = append(out, sandbox.Handle{ID: id})
		}
	}
	return out
}

// FindByRunID returns the live handles created with RunID runID.
func (p *Pausable) FindByRunID(_ context.Context, runID string) ([]sandbox.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []sandbox.Handle
	for _, id := range p.order {
		if !p.removed[id] && p.boxes[id].RunID == runID {
			out = append(out, sandbox.Handle{ID: id})
		}
	}
	return out, nil
}

// Close returns nil.
func (p *Pausable) Close() error { return nil }

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
