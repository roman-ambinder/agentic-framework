package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// openExistingLog opens an event log for reading. Unlike eventlog.Open it
// refuses a path that does not exist, so a typo in --db is reported instead
// of silently creating an empty log.
func openExistingLog(ctx context.Context, path string) (*eventlog.Store, error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("event log %s does not exist", path)
	}
	return eventlog.Open(ctx, path)
}
