package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/roman-ambinder/agentic-framework/internal/controlplane"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
)

// defaultControlDBPath is the default --control-db of every command that uses
// the control database (m2.md decision 2).
const defaultControlDBPath = ".adlc/control.db"

// openControlDB opens the control db, creating its directory and file
// (used by commands that write: kill).
func openControlDB(ctx context.Context, path string) (*controlplane.Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("control db: create directory: %w", err)
	}
	return controlplane.Open(ctx, path)
}

// openExistingControlDB refuses a path that does not exist.
func openExistingControlDB(ctx context.Context, path string) (*controlplane.Store, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("control db %s does not exist", path)
	}
	return controlplane.Open(ctx, path)
}

// openKillSwitch opens the control db for a run. A missing file gives a nil
// KillSwitch, a no-op close and no error; the file is never created.
// Any other failure: fmt.Errorf("run: control db: %w", err).
func openKillSwitch(ctx context.Context, path string) (harness.KillSwitch, func(), error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, func() {}, nil // an untyped nil interface
		}
		return nil, func() {}, fmt.Errorf("run: control db: %w", err)
	}
	store, err := controlplane.Open(ctx, path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("run: control db: %w", err)
	}
	return store, func() { _ = store.Close() }, nil
}
