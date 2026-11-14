package harness

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// ForkPoint names the run and the step a fork starts from.
type ForkPoint struct {
	RunID  string // origin run
	AtStep uint32 // >= 1; the origin must have a checkpoint for this step
}

var (
	// ErrNoCheckpoint means the origin run has no checkpoint for the step.
	ErrNoCheckpoint = errors.New("harness: no checkpoint for that step")
	// ErrRunExists means the fork's run id is already in the log.
	ErrRunExists = errors.New("harness: run already exists")
)

// ForkEvents returns the events a fork copies: the origin's events from seq 1
// up to and including its first checkpoint with Step == atStep. It refuses a
// prefix whose last checkpoint does not match the log (ErrCheckpointMismatch).
func ForkEvents(events []*adlcv1.Event, atStep uint32) ([]*adlcv1.Event, error) {
	if atStep == 0 {
		return nil, errors.New("harness: fork: step must be at least 1")
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: no events", eventlog.ErrRunNotFound)
	}
	i := -1
	for j, ev := range events {
		if cp := ev.GetCheckpoint(); cp != nil && cp.GetStep() == atStep {
			i = j
			break
		}
	}
	if i < 0 {
		return nil, fmt.Errorf("%w: run %q, step %d (checkpointed steps: %s)",
			ErrNoCheckpoint, events[0].GetRunId(), atStep, checkpointSteps(events))
	}
	prefix := events[:i+1]
	state, err := eventlog.Fold(prefix)
	if err != nil {
		return nil, fmt.Errorf("harness: fork: %w", err)
	}
	if err := verifyLastCheckpoint(prefix, state); err != nil {
		return nil, err
	}
	return prefix, nil
}

// checkpointSteps lists the distinct checkpoint steps in log order, or "none".
func checkpointSteps(events []*adlcv1.Event) string {
	var steps []string
	seen := map[uint32]bool{}
	for _, ev := range events {
		if cp := ev.GetCheckpoint(); cp != nil && !seen[cp.GetStep()] {
			seen[cp.GetStep()] = true
			steps = append(steps, strconv.FormatUint(uint64(cp.GetStep()), 10))
		}
	}
	if len(steps) == 0 {
		return "none"
	}
	return strings.Join(steps, ", ")
}

// Fork starts the run cfg.RunID from the transcript of from.RunID up to and
// including step from.AtStep and continues it with cfg and deps. cfg.Task is
// ignored. Like Run, the error is non-nil only for failures to use the log
// and for refusing the fork; every other failure ends the run with a stop
// reason.
//
// Only the conversation is forked; a sandbox workspace is not restored.
//   - Cost: copied events carry no Cost, so the money is counted once, by the
//     origin. The fork's Usage and its max_usd / max_tokens budget count only
//     its own calls.
//   - Counted limits: max_steps and max_tool_calls count the copied steps and
//     tool calls too; the wall clock starts at the fork.
//   - Times: copied events get the time they are copied at; latencies and
//     durations inside payloads are kept.
//   - Model change: copied provider blobs are replayed by Transcript and
//     adapters drop blobs of other providers, so switching provider is safe.
//   - Tools: copied tool calls name the origin's tools; if the new agent lacks
//     one, the model only sees it in history.
func Fork(ctx context.Context, cfg Config, deps Deps, from ForkPoint) (*eventlog.RunState, error) {
	cfg, deps = applyDefaults(cfg, deps)
	if err := validate(cfg, deps); err != nil {
		return nil, err
	}
	cfg.Context = effectiveBudget(cfg.Context, deps.Model.Capabilities())
	origin, err := deps.Log.Read(ctx, from.RunID, 0)
	if err != nil {
		return nil, fmt.Errorf("harness: fork: read log: %w", err)
	}
	if len(origin) == 0 {
		return nil, fmt.Errorf("%w: %q", eventlog.ErrRunNotFound, from.RunID)
	}
	prefix, err := ForkEvents(origin, from.AtStep)
	if err != nil {
		return nil, err
	}
	existing, err := deps.Log.Read(ctx, cfg.RunID, 0)
	if err != nil {
		return nil, fmt.Errorf("harness: fork: read log: %w", err)
	}
	if len(existing) > 0 {
		return nil, fmt.Errorf("%w: %q", ErrRunExists, cfg.RunID)
	}
	return newLoop(ctx, cfg, deps).fork(prefix, from)
}

// fork writes the copied prefix to the log under the fork's run id and
// continues the loop at the next step.
func (l *loop) fork(prefix []*adlcv1.Event, from ForkPoint) (*eventlog.RunState, error) {
	l.startedAt = l.deps.Now()
	cancel := l.startWallClock()
	defer cancel()
	if _, err := l.add(&adlcv1.Event{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{
		AgentVersion: l.cfg.AgentVersion, Task: prefix[0].GetRunStarted().GetTask(), Budget: l.cfg.Budget,
		ForkedFromRunId: from.RunID, ForkedAtStep: from.AtStep, SandboxImage: l.cfg.SandboxImage,
	}}}); err != nil {
		return nil, err
	}
	for _, ev := range prefix[1:] {
		if err := l.copyEvent(ev); err != nil {
			return nil, err
		}
	}
	l.loadTools()
	return l.steps(uint32(l.r.modelCalls) + 1) //nolint:gosec // step count is small
}

// copyEvent appends the fork's version of one origin event.
func (l *loop) copyEvent(ev *adlcv1.Event) error {
	var err error
	if cp := ev.GetCheckpoint(); cp != nil {
		// The origin's digest covers another run id and other times.
		err = l.writeCheckpoint(cp.GetStep())
	} else {
		c := proto.CloneOf(ev)
		c.Seq, c.Time, c.Cost = 0, nil, nil
		_, err = l.add(c)
	}
	if err != nil {
		return err
	}
	if got := l.r.state.LastSeq; got != ev.GetSeq() {
		return fmt.Errorf("harness: fork: copied seq %d stored as %d", ev.GetSeq(), got)
	}
	return nil
}
