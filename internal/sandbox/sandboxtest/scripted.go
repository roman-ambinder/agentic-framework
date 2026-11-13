package sandboxtest

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// Scripted is a Sandbox that returns canned results. Create returns
// Handle{ID: "scripted"}, CopyOut returns an empty tar and Destroy returns nil.
type Scripted struct {
	// Results maps strings.Join(cmd, " ") to the result to return.
	Results map[string]sandbox.ExecResult

	mu    sync.Mutex
	calls [][]string
}

var _ sandbox.Sandbox = (*Scripted)(nil)

// Create returns Handle{ID: "scripted"}.
func (s *Scripted) Create(context.Context, sandbox.Spec) (sandbox.Handle, error) {
	return sandbox.Handle{ID: "scripted"}, nil
}

// Exec records cmd and returns the result scripted for it.
func (s *Scripted) Exec(_ context.Context, _ sandbox.Handle, cmd []string, _ time.Duration) (sandbox.ExecResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, append([]string(nil), cmd...))
	key := strings.Join(cmd, " ")
	res, ok := s.Results[key]
	if !ok {
		return sandbox.ExecResult{}, fmt.Errorf("sandboxtest: no scripted result for %q", key)
	}
	return res, nil
}

// Calls returns every command passed to Exec, in order.
func (s *Scripted) Calls() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.calls))
	for i, c := range s.calls {
		out[i] = append([]string(nil), c...)
	}
	return out
}

// CopyOut returns an empty tar archive.
func (s *Scripted) CopyOut(context.Context, sandbox.Handle, string) ([]byte, error) {
	var buf bytes.Buffer
	if err := tar.NewWriter(&buf).Close(); err != nil {
		return nil, fmt.Errorf("sandboxtest: empty tar: %w", err)
	}
	return buf.Bytes(), nil
}

// Destroy does nothing.
func (s *Scripted) Destroy(context.Context, sandbox.Handle) error { return nil }
