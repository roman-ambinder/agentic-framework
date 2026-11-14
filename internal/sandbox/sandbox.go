// Package sandbox defines the interface every sandbox backend implements,
// plus the labels, limits and helpers they share.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// SandboxIDPlaceholder in a tool server's command line is replaced with the
// sandbox's id by whoever starts the servers after creating the sandbox.
const SandboxIDPlaceholder = "{{sandbox_id}}"

var (
	// ErrInvalidSpec means Create was called with a bad Spec.
	ErrInvalidSpec = errors.New("sandbox: invalid spec")
	// ErrNotFound means the handle does not name a live sandbox.
	ErrNotFound = errors.New("sandbox: not found")
	// ErrSnapshotChanged means the snapshot file no longer matches its SHA256.
	ErrSnapshotChanged = errors.New("sandbox: snapshot file changed")
	// ErrImageGone means the image a snapshot was taken from no longer exists.
	ErrImageGone = errors.New("sandbox: snapshot image is gone")
)

// ProcessesNotSupported is Snapshot.ProcessesSkipped while processes are
// never kept.
const ProcessesNotSupported = "process checkpoints are not supported yet"

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

// Snapshot describes a saved /workspace.
type Snapshot struct {
	TarPath          string // absolute path of the /workspace archive
	SHA256           string // "sha256:<hex>" of the file at TarPath
	Bytes            int64
	ImageID          string // image ID the container ran, e.g. "sha256:..."
	ProcessesSkipped string // why processes were not kept
}

// Suspender is implemented by backends that can save /workspace to a file and
// free the sandbox, then bring it back later (files only).
type Suspender interface {
	// Suspend writes the /workspace archive of h to tarPath, destroys the
	// sandbox and returns the snapshot.
	Suspend(ctx context.Context, h Handle, tarPath string) (Snapshot, error)
	// Restore creates a sandbox for spec and fills /workspace from snap. It
	// returns ErrSnapshotChanged or ErrImageGone when snap cannot be used.
	Restore(ctx context.Context, spec Spec, snap Snapshot) (Handle, error)
}

// ImageResolver is implemented by backends that can tell which image a
// sandbox runs.
type ImageResolver interface {
	// ImageID returns the content ID ("sha256:<hex>") of the image the
	// sandbox h was created from. The ID must be usable as Spec.Image for
	// the same backend: the eval runner creates the grading sandbox from it.
	ImageID(ctx context.Context, h Handle) (string, error)
}

// ShortImageID shortens an image ID of the form "sha256:" plus 64 hex
// characters to "sha256:" plus its first 12. Any other string is returned
// unchanged.
func ShortImageID(id string) string {
	const prefix = "sha256:"
	hexPart, ok := strings.CutPrefix(id, prefix)
	if !ok || len(hexPart) != 64 {
		return id
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return id
		}
	}
	return prefix + hexPart[:12]
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

// ExpandArgs returns a copy of args with every SandboxIDPlaceholder (also
// inside a longer argument) replaced by h.ID.
func ExpandArgs(args []string, h Handle) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, SandboxIDPlaceholder, h.ID)
	}
	return out
}
