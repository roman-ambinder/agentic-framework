package harness

import (
	"context"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// recorder appends the run's events to the log, keeps them in memory and
// applies them to a RunState.
type recorder struct {
	ctx     context.Context // context.WithoutCancel(parent): log writes survive cancellation
	log     Log
	runID   string
	agentID string
	events  []*adlcv1.Event   // every event appended, as stored
	state   eventlog.RunState // events applied in order

	modelCalls int // model_call_completed events recorded
}

func newRecorder(parent context.Context, log Log, runID, agentID string) *recorder {
	return &recorder{ctx: context.WithoutCancel(parent), log: log, runID: runID, agentID: agentID}
}

// newRecorderFrom returns a recorder that already holds events (as read from
// the log) and the state folded from them.
func newRecorderFrom(ctx context.Context, log Log, runID, agentID string, events []*adlcv1.Event, state eventlog.RunState) *recorder {
	r := newRecorder(ctx, log, runID, agentID)
	r.events = events
	r.state = state
	for _, ev := range events {
		if ev.GetModelCallCompleted() != nil {
			r.modelCalls++
		}
	}
	return r
}

// add sets RunId and AgentId on ev, appends it to the log, applies the stored
// event to r.state and returns its seq. Any error is returned as is. A failure
// from state.Apply means the harness wrote an invalid sequence (a bug).
func (r *recorder) add(ev *adlcv1.Event) (uint64, error) {
	ev.RunId = r.runID
	ev.AgentId = r.agentID
	stored, err := r.log.Append(r.ctx, ev)
	if err != nil {
		return 0, err
	}
	if err := r.state.Apply(stored); err != nil {
		return 0, err
	}
	r.events = append(r.events, stored)
	if stored.GetModelCallCompleted() != nil {
		r.modelCalls++
	}
	return stored.GetSeq(), nil
}
