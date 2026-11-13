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
// Elided results are remembered by position (message index, part index), not
// by tool call ID: providers do not guarantee unique call IDs across a run.
// This relies on messages only ever being appended.
//
// A Manager is for one run and is not safe for concurrent use. A resumed run
// (task 1.7) starts with a new Manager, so its first request may miss the
// prompt cache once; the elided set is not persisted.
type Manager struct {
	Budget Budget
	elided map[location]bool // positions of the tool results that are elided
}

// NewManager returns a Manager for one run that uses budget b.
func NewManager(b Budget) *Manager {
	return &Manager{Budget: b, elided: map[location]bool{}}
}

// Prepare is Build plus memory: first it elides every result at a location in
// m.elided (these are not counted as new elisions), then it runs the Build
// algorithm for anything more, and finally records the newly elided locations.
// The request m.Budget is applied to is req; req itself is never modified.
func (m *Manager) Prepare(ctx context.Context, req *model.Request) (*model.Request, Stats, error) {
	if m.elided == nil {
		m.elided = map[location]bool{}
	}
	out, st, newly, err := build(ctx, req, m.Budget, m.elided)
	for _, id := range newly {
		m.elided[id] = true
	}
	return out, st, err
}
