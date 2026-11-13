package harness

import (
	"context"
	"errors"
	"fmt"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/contextmgr"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

var (
	// ErrRunFinished means the run already has a final run_completed.
	ErrRunFinished = errors.New("harness: run already finished")
	// ErrRunPaused means the run is waiting for an approval; resolve it instead.
	ErrRunPaused = errors.New("harness: run is waiting for approval")
	// ErrCheckpointMismatch means the log does not match its last checkpoint.
	ErrCheckpointMismatch = errors.New("harness: checkpoint does not match the log")
	// ErrAgentMismatch means Config.AgentVersion differs from the run's run_started.
	ErrAgentMismatch = errors.New("harness: agent version differs from the run")
)

// AbandonedOutput is the tool result for a call that was running when the
// process died. The call may or may not have taken effect.
const AbandonedOutput = "abandoned: the run stopped while this tool call was running; it may or may not have taken effect. Check before repeating it."

// NotRunOutput is the tool result for a call that was never started.
const NotRunOutput = "not run: the run stopped before this tool call started. Call it again if it is still needed."

// resumeReason is run_resumed.reason.
const resumeReason = "process stopped without run_completed"

// Resume continues the run cfg.RunID, which must exist in deps.Log and must
// have stopped without a final run_completed. cfg.Task is ignored: the task
// comes from the run's run_started event. Unknown run → error wrapping
// eventlog.ErrRunNotFound.
//
// Resume only appends: it closes what was in flight when the process died
// (see recoveryPlan), then continues the normal loop. Like Run, it returns an
// error only for failures to use the event log or for refusing the run.
func Resume(ctx context.Context, cfg Config, deps Deps) (*eventlog.RunState, error) {
	cfg, deps = applyDefaults(cfg, deps)
	if err := validate(cfg, deps); err != nil {
		return nil, err
	}
	cfg.Context = effectiveBudget(cfg.Context, deps.Model.Capabilities())
	events, state, err := loadForResume(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	l := newResumeLoop(ctx, cfg, deps, events, state)
	return l.resume()
}

// loadForResume reads and folds the run's log and refuses runs that cannot be
// resumed.
func loadForResume(ctx context.Context, cfg Config, deps Deps) ([]*adlcv1.Event, eventlog.RunState, error) {
	events, err := deps.Log.Read(ctx, cfg.RunID, 0)
	if err != nil {
		return nil, eventlog.RunState{}, fmt.Errorf("harness: resume: read log: %w", err)
	}
	if len(events) == 0 {
		return nil, eventlog.RunState{}, fmt.Errorf("%w: %q", eventlog.ErrRunNotFound, cfg.RunID)
	}
	state, err := eventlog.Fold(events)
	if err != nil {
		return nil, eventlog.RunState{}, fmt.Errorf("harness: resume: %w", err)
	}
	switch {
	case state.Status == eventlog.StatusFinished:
		return nil, eventlog.RunState{}, ErrRunFinished
	case state.Status == eventlog.StatusPaused:
		return nil, eventlog.RunState{}, ErrRunPaused
	case cfg.AgentVersion != state.AgentVersion:
		return nil, eventlog.RunState{}, fmt.Errorf("%w: run has %q, config has %q",
			ErrAgentMismatch, state.AgentVersion, cfg.AgentVersion)
	}
	if err := verifyLastCheckpoint(events, state); err != nil {
		return nil, eventlog.RunState{}, err
	}
	return events, *state, nil
}

// verifyLastCheckpoint checks that the last checkpoint's digest is the digest
// of the state folded from every event before it.
func verifyLastCheckpoint(events []*adlcv1.Event, state *eventlog.RunState) error {
	cp := state.LastCheckpoint
	if cp == nil {
		return nil
	}
	before, err := eventlog.Fold(events[:cp.Seq-1]) // the event with seq s is events[s-1]
	if err != nil {
		return fmt.Errorf("harness: resume: %w", err)
	}
	d, err := before.Digest()
	if err != nil {
		return fmt.Errorf("harness: resume: %w", err)
	}
	if d != cp.StateDigest {
		return fmt.Errorf("%w: checkpoint at seq %d", ErrCheckpointMismatch, cp.Seq)
	}
	return nil
}

// newResumeLoop is newLoop for a run that already has events.
func newResumeLoop(ctx context.Context, cfg Config, deps Deps, events []*adlcv1.Event, state eventlog.RunState) *loop {
	return &loop{
		ctx: ctx, callCtx: ctx, cfg: cfg, deps: deps,
		r:   newRecorderFrom(ctx, deps.Log, cfg.RunID, cfg.AgentID, events, state),
		mgr: contextmgr.NewManager(cfg.Context),
	}
}

// resume appends the recovery events and continues the loop.
func (l *loop) resume() (*eventlog.RunState, error) {
	st := l.r.state
	// The wall clock keeps the time the run already used; every other budget
	// counter comes from the folded state (see usage).
	l.startedAt = l.deps.Now().Add(-st.LastEventAt.Sub(st.StartedAt))
	cancel := l.startWallClock()
	defer cancel()
	l.loadTools()
	plan := l.planRecovery()
	nextStep := uint32(l.r.modelCalls) + 1 //nolint:gosec // step count is small
	var verified uint64
	if st.LastCheckpoint != nil {
		verified = st.LastCheckpoint.Seq
	}
	if _, err := l.add(&adlcv1.Event{Payload: &adlcv1.Event_RunResumed{RunResumed: &adlcv1.RunResumed{
		NextStep: nextStep, VerifiedCheckpointSeq: verified,
		AbandonedToolCalls: uint32(plan.closed()), //nolint:gosec // call count is small
		Reason:             resumeReason,
	}}}); err != nil {
		return nil, err
	}
	if plan.finish != nil {
		err := l.finishWith(plan.finish)
		return l.final, err
	}
	if err := l.closeInFlight(plan); err != nil {
		return nil, err
	}
	if l.cfg.Plan.Enabled && l.r.modelCalls == 0 && !hasPlan(l.r.events) {
		if err := l.runPlanCall(); err != nil || l.final != nil {
			return l.final, err
		}
	}
	return l.steps(nextStep)
}

// recoveryPlan is what Resume appends after run_resumed. Either finish is
// set (the run ends), or the calls are closed and the loop continues.
type recoveryPlan struct {
	finish    *adlcv1.RunCompleted
	abandoned []abandonedCall    // requested, never completed: case A
	notRun    []*adlcv1.ToolCall // in the last response, never requested: case B
}

type abandonedCall struct {
	id     string
	reqSeq uint64
}

// closed is the number of tool results the plan appends.
func (p recoveryPlan) closed() int { return len(p.abandoned) + len(p.notRun) }

// planRecovery decides, from the events after the last model_call_completed
// and from the state, what was in flight. Exactly one case applies, checked in
// the order E, G, F, A, B, C, D (docs/impl/tasks/1.7-resume.md).
func (l *loop) planRecovery() recoveryPlan {
	events, state := l.r.events, &l.r.state
	if len(state.PendingApprovals) > 0 { // case E
		return recoveryPlan{finish: &adlcv1.RunCompleted{StopReason: adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL}}
	}
	last := events[len(events)-1]
	if pd := last.GetPolicyDecided(); pd != nil && pd.GetDecision() == adlcv1.Decision_DECISION_DENY { // case G
		return recoveryPlan{finish: &adlcv1.RunCompleted{
			StopReason: adlcv1.StopReason_STOP_REASON_DENIED,
			Detail:     fmt.Sprintf("rule %s: %s", pd.GetRuleId(), pd.GetReason()),
		}}
	}
	mc, tail := lastModelCompleted(events)
	if mc == nil {
		return recoveryPlan{} // case C or D: no response was recorded yet
	}
	if rc := responseOutcome(mc, tail); rc != nil { // case F
		return recoveryPlan{finish: rc}
	}
	return recoveryPlan{abandoned: l.abandonedCalls(), notRun: notRequested(mc, tail)} // cases A, B; else C, D
}

// lastModelCompleted returns the last model_call_completed of a step and the
// events after it, or nil and the whole log if there is none. The planning
// call (task Q.4) is not a step response: it is skipped, so Resume and
// Approve never treat the plan as the run's answer.
func lastModelCompleted(events []*adlcv1.Event) (*adlcv1.ModelCallCompleted, []*adlcv1.Event) {
	for i := len(events) - 1; i >= 0; i-- {
		if mc := events[i].GetModelCallCompleted(); mc != nil {
			if mc.GetCallId() == PlanCallID {
				continue
			}
			return mc, events[i+1:]
		}
	}
	return nil, events
}

// responseOutcome returns the run_completed that afterResponse would have
// written for mc, if the process died before writing it: that is, when mc ends
// the run and only checkpoints follow it. It returns nil otherwise.
//
// The brief's case F only names END_TURN without tool calls; the loop also
// stops on a refusal and on max_tokens, and a request that ends with the
// assistant's own message must never be sent to the model.
func responseOutcome(mc *adlcv1.ModelCallCompleted, tail []*adlcv1.Event) *adlcv1.RunCompleted {
	for _, ev := range tail {
		if ev.GetCheckpoint() == nil {
			return nil
		}
	}
	switch {
	case mc.GetFinishReason() == adlcv1.FinishReason_FINISH_REASON_REFUSAL:
		return &adlcv1.RunCompleted{StopReason: adlcv1.StopReason_STOP_REASON_ERROR, Detail: "model refused"}
	case len(mc.GetToolCalls()) > 0:
		return nil
	case mc.GetFinishReason() == adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS:
		return &adlcv1.RunCompleted{StopReason: adlcv1.StopReason_STOP_REASON_ERROR, Detail: "output limit reached"}
	default:
		return &adlcv1.RunCompleted{StopReason: adlcv1.StopReason_STOP_REASON_COMPLETED, Result: mc.GetText()}
	}
}

// abandonedCalls lists the pending tool calls with the seq of their request.
func (l *loop) abandonedCalls() []abandonedCall {
	var out []abandonedCall
	for _, id := range l.r.state.PendingToolCalls {
		for _, ev := range l.r.events {
			if c := ev.GetToolCallRequested().GetCall(); c != nil && c.GetId() == id {
				out = append(out, abandonedCall{id: id, reqSeq: ev.GetSeq()})
				break
			}
		}
	}
	return out
}

// notRequested returns the calls of mc that have no tool_call_requested in
// the events after it, in the response's order.
func notRequested(mc *adlcv1.ModelCallCompleted, tail []*adlcv1.Event) []*adlcv1.ToolCall {
	requested := map[string]bool{}
	for _, ev := range tail {
		if c := ev.GetToolCallRequested().GetCall(); c != nil {
			requested[c.GetId()] = true
		}
	}
	var out []*adlcv1.ToolCall
	for _, c := range mc.GetToolCalls() {
		if !requested[c.GetId()] {
			out = append(out, c)
		}
	}
	return out
}

// closeInFlight appends an error result for every abandoned call, then a
// request and an error result for every call that was never started.
func (l *loop) closeInFlight(p recoveryPlan) error {
	for _, a := range p.abandoned {
		if err := l.addErrorResult(a.id, a.reqSeq, AbandonedOutput); err != nil {
			return err
		}
	}
	for _, c := range p.notRun {
		tool := l.byName[c.GetName()] // unknown tool: zero Risk and Server
		reqSeq, err := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ToolCallRequested{ToolCallRequested: &adlcv1.ToolCallRequested{
			Call: c, RiskClass: tool.Risk, Server: tool.Server,
		}}})
		if err != nil {
			return err
		}
		if err := l.addErrorResult(c.GetId(), reqSeq, NotRunOutput); err != nil {
			return err
		}
	}
	return nil
}

func (l *loop) addErrorResult(callID string, reqSeq uint64, output string) error {
	_, err := l.add(&adlcv1.Event{ParentSeq: reqSeq, Payload: &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{
		CallId: callID, IsError: true, Output: []byte(output),
	}}})
	return err
}
