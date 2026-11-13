package contextmgr

import (
	"context"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// Manager builds the requests of one run and remembers which tool results it
// has elided, so that they stay elided on later steps. Between two elision
// batches every request then starts with the previous request's bytes, which
// lets the provider's prompt cache hit.
//
// A Manager is for one run and is not safe for concurrent use. A resumed run
// (task 1.7) starts with a new Manager, so its first request may miss the
// prompt cache once; the elided set is not persisted.
type Manager struct {
	Budget Budget
	elided map[string]bool // tool call IDs whose result is elided
}

// NewManager returns a Manager for one run that uses budget b.
func NewManager(b Budget) *Manager {
	return &Manager{Budget: b, elided: map[string]bool{}}
}

// Prepare is Build plus memory: first it elides every result whose CallID is
// in m.elided (these are not counted as new elisions), then it runs the Build
// algorithm for anything more, and finally records the newly elided CallIDs.
// The request m.Budget is applied to is req; req itself is never modified.
func (m *Manager) Prepare(ctx context.Context, req *model.Request) (*model.Request, Stats, error) {
	if m.elided == nil {
		m.elided = map[string]bool{}
	}
	out, st, newly, err := build(ctx, req, m.Budget, m.elided)
	for _, id := range newly {
		m.elided[id] = true
	}
	return out, st, err
}
