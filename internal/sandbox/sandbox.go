// Package sandbox defines the interface every sandbox backend implements,
// plus the labels, limits and helpers they share.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Labels put on every container this project creates.
const (
	// LabelManaged marks a container as created by this project (value "true").
	LabelManaged = "adlc.managed"
	// LabelRunID holds the id of the run that owns the container.
	LabelRunID = "adlc.run_id"
)

// WorkspacePath is where the workspace lives inside every sandbox.
const WorkspacePath = "/workspace"

// SandboxUID and SandboxGID are the user every sandbox process runs as.
const (
	SandboxUID = 65532
	SandboxGID = 65532
)

// PidsLimit caps the number of processes in a sandbox.
const PidsLimit = 512

// MaxWorkspaceBytes caps the total size of files copied into a sandbox.
const MaxWorkspaceBytes = 512 << 20 // 512 MiB

// DefaultHome is HOME inside a sandbox unless Spec.Env sets it.
const DefaultHome = "/tmp"

// MaxOutputBytes caps ExecResult.Stdout and ExecResult.Stderr each.
const MaxOutputBytes = 8 << 20 // 8 MiB

// TruncatedMarker is appended to Stdout/Stderr when they were capped.
const TruncatedMarker = "\n[output truncated by sandbox]\n"

var (
	// ErrInvalidSpec means Create was called with a bad Spec.
	ErrInvalidSpec = errors.New("sandbox: invalid spec")
	// ErrNotFound means the handle does not name a live sandbox.
	ErrNotFound = errors.New("sandbox: not found")
)

// Spec describes the sandbox to create.
type Spec struct {
	RunID        string            // required; stored as label adlc.run_id
	Image        string            // required, e.g. "golang:1.26"
	WorkspaceDir string            // host dir copied into /workspace; "" = empty /workspace
	CPUs         float64           // 0 = no limit
	MemoryBytes  int64             // 0 = no limit
	Network      bool              // false = no network
	Env          map[string]string // the only environment the container gets
}

// Validate checks the fields every backend needs.
func (s Spec) Validate() error {
	var reason string
	switch {
	case s.RunID == "":
		reason = "run id is empty"
	case s.Image == "":
		reason = "image is empty"
	case s.CPUs < 0:
		reason = "cpus must not be negative"
	case s.MemoryBytes < 0:
		reason = "memory must not be negative"
	default:
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidSpec, reason)
}

// ExecResult is the outcome of a command that ran to completion or timed out.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Duration time.Duration
	TimedOut bool
}

// Handle names one sandbox.
type Handle struct{ ID string }

// Sandbox is an isolated place to run commands.
type Sandbox interface {
	// Create starts a sandbox for spec and returns its handle.
	Create(ctx context.Context, spec Spec) (Handle, error)
	// Exec runs the argv list cmd with working directory WorkspacePath.
	//
	// timeout must be > 0. On expiry the command is killed and the call
	// returns normally with TimedOut true and a non-zero ExitCode. A command
	// that runs and exits non-zero is NOT an error: the error return is only
	// for "could not run at all" (sandbox gone, daemon down, ctx cancelled by
	// the caller). Stdout and Stderr are capped with
	// CapOutput(..., MaxOutputBytes).
	Exec(ctx context.Context, h Handle, cmd []string, timeout time.Duration) (ExecResult, error)
	// CopyOut returns a tar archive of path (inside the sandbox).
	CopyOut(ctx context.Context, h Handle, path string) ([]byte, error)
	// Destroy removes the sandbox.
	Destroy(ctx context.Context, h Handle) error
}

// CapOutput returns b unchanged if len(b) <= max, otherwise the first max
// bytes followed by TruncatedMarker.
func CapOutput(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	out := make([]byte, 0, max+len(TruncatedMarker))
	out = append(out, b[:max]...)
	return append(out, TruncatedMarker...)
}
