package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

var (
	// ErrRunNotPaused means the run is not waiting for an approval.
	ErrRunNotPaused = errors.New("harness: run is not waiting for approval")
	// ErrUnknownApproval means the run has no approval_requested with this id.
	ErrUnknownApproval = errors.New("harness: unknown approval")
	// ErrApprovalResolved means the approval already has an approval_resolved.
	ErrApprovalResolved = errors.New("harness: approval already resolved")
	// ErrContinueRun means ResolveApproval was asked to resolve the last
	// pending approval; that must go through Approve, which continues the run.
	ErrContinueRun = errors.New("harness: last pending approval: the run must be continued with Approve")
)

var errNoResolver = errors.New("harness: approve: resolved by is empty")

// Resolution is a person's answer to one pending approval.
type Resolution struct {
	ApprovalID string
	Approved   bool
	By         string // required: who resolved it
	Comment    string // optional
}

// RejectedOutput is the tool result of a rejected call:
// "rejected by <by>" or, with a comment, "rejected by <by>: <comment>".
func RejectedOutput(by, comment string) string {
	if comment == "" {
		return "rejected by " + by
	}
	return "rejected by " + by + ": " + comment
}

// CheckApproval folds a run's events and checks that approvalID can be
// resolved now. It returns the folded state. Checks, in this order:
// no events → eventlog.ErrRunNotFound; no approval_requested with that id →
// ErrUnknownApproval; an approval_resolved with that id → ErrApprovalResolved;
// status not paused → ErrRunNotPaused.
func CheckApproval(events []*adlcv1.Event, runID, approvalID string) (*eventlog.RunState, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: %q", eventlog.ErrRunNotFound, runID)
	}
	state, err := eventlog.Fold(events)
	if err != nil {
		return nil, fmt.Errorf("harness: approve: %w", err)
	}
	var requested, resolved bool
	for _, ev := range events {
		if ev.GetApprovalRequested().GetApprovalId() == approvalID {
			requested = true
		}
		if ev.GetApprovalResolved().GetApprovalId() == approvalID {
			resolved = true
		}
	}
	switch {
	case !requested:
		return nil, fmt.Errorf("%w: %q in run %q", ErrUnknownApproval, approvalID, runID)
	case resolved:
		return nil, fmt.Errorf("%w: %q", ErrApprovalResolved, approvalID)
	case state.Status != eventlog.StatusPaused:
		return nil, fmt.Errorf("%w: run %q is %s", ErrRunNotPaused, runID, state.Status)
	}
	return state, nil
}

// requestSeq returns the seq of the approval_requested event for approvalID.
func requestSeq(events []*adlcv1.Event, approvalID string) uint64 {
	for _, ev := range events {
		if ev.GetApprovalRequested().GetApprovalId() == approvalID {
			return ev.GetSeq()
		}
	}
	return 0
}

// resolvedEvent builds the approval_resolved event for res.
func resolvedEvent(events []*adlcv1.Event, res Resolution) *adlcv1.Event {
	return &adlcv1.Event{
		ParentSeq: requestSeq(events, res.ApprovalID),
		Payload: &adlcv1.Event_ApprovalResolved{ApprovalResolved: &adlcv1.ApprovalResolved{
			ApprovalId: res.ApprovalID, Approved: res.Approved, ResolvedBy: res.By, Comment: res.Comment,
		}},
	}
}

// ResolveApproval records res for a run that will still wait for other
// approvals afterwards: it appends approval_resolved and then
// run_completed{AWAITING_APPROVAL}. If res resolves the last pending approval
// it appends nothing and returns ErrContinueRun.
func ResolveApproval(ctx context.Context, log Log, runID string, res Resolution) (*eventlog.RunState, error) {
	if res.By == "" {
		return nil, errNoResolver
	}
	events, err := log.Read(ctx, runID, 0)
	if err != nil {
		return nil, fmt.Errorf("harness: approve: read log: %w", err)
	}
	state, err := CheckApproval(events, runID, res.ApprovalID)
	if err != nil {
		return nil, err
	}
	if len(state.PendingApprovals) == 1 {
		return nil, ErrContinueRun
	}
	r := newRecorderFrom(ctx, log, runID, state.AgentID, events, *state)
	if _, err := r.add(resolvedEvent(events, res)); err != nil {
		return nil, fmt.Errorf("harness: %w", err)
	}
	if _, err := r.add(&adlcv1.Event{Payload: &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{
		StopReason: adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL,
	}}}); err != nil {
		return nil, fmt.Errorf("harness: %w", err)
	}
	st := r.state
	return &st, nil
}

// Approve records res and, if no approval is left pending, continues the run
// in place. cfg.Task is ignored. Errors as CheckApproval, plus
// ErrAgentMismatch and ErrCheckpointMismatch exactly as Resume reports them.
func Approve(ctx context.Context, cfg Config, deps Deps, res Resolution) (*eventlog.RunState, error) {
	if res.By == "" {
		return nil, errNoResolver
	}
	cfg, deps = applyDefaults(cfg, deps)
	if err := validate(cfg, deps); err != nil {
		return nil, err
	}
	cfg.Context = effectiveBudget(cfg.Context, deps.Model.Capabilities())
	events, err := deps.Log.Read(ctx, cfg.RunID, 0)
	if err != nil {
		return nil, fmt.Errorf("harness: approve: read log: %w", err)
	}
	state, err := CheckApproval(events, cfg.RunID, res.ApprovalID)
	if err != nil {
		return nil, err
	}
	if cfg.AgentVersion != state.AgentVersion {
		return nil, fmt.Errorf("%w: run has %q, config has %q",
			ErrAgentMismatch, state.AgentVersion, cfg.AgentVersion)
	}
	if err := verifyLastCheckpoint(events, state); err != nil {
		return nil, err
	}
	if len(state.PendingApprovals) > 1 {
		return ResolveApproval(ctx, deps.Log, cfg.RunID, res)
	}
	plan, err := planApproval(events, res)
	if err != nil {
		return nil, err
	}
	l := newResumeLoop(ctx, cfg, deps, events, *state)
	used := state.LastEventAt.Sub(state.StartedAt)
	l.startedAt = deps.Now().Add(-used)
	cancel := l.startWallClock()
	defer cancel()
	l.loadTools()
	if _, err := l.add(resolvedEvent(events, res)); err != nil {
		return nil, err
	}
	if err := l.runPlan(plan); err != nil || l.final != nil {
		return l.final, err
	}
	if err := l.writeCheckpoint(l.r.state.Step); err != nil {
		return nil, err
	}
	return l.steps(uint32(l.r.modelCalls) + 1) //nolint:gosec // step count is small
}

// runPlan handles every call of the paused step, in order.
func (l *loop) runPlan(plan []approvalItem) error {
	for _, item := range plan {
		var err error
		switch item.kind {
		case itemApproved:
			err = l.runApproved(item.call, item.reqSeq)
		case itemRejected:
			err = l.addErrorResult(item.call.ID, item.reqSeq, RejectedOutput(item.by, item.comment))
		case itemNotRun:
			err = l.runToolCall(item.call)
		case itemDone:
		}
		if err != nil || l.final != nil {
			return err
		}
	}
	return nil
}

type itemKind int

const (
	itemDone itemKind = iota
	itemApproved
	itemRejected
	itemNotRun
)

// approvalItem is what Approve does with one call of the paused step.
type approvalItem struct {
	kind    itemKind
	call    model.ToolCall
	reqSeq  uint64 // seq of the call's tool_call_requested
	by      string
	comment string
}

// planApproval decides, from the events after the last model response, what
// to do with each of its calls. It appends nothing.
func planApproval(events []*adlcv1.Event, res Resolution) ([]approvalItem, error) {
	mc, tail := lastModelCompleted(events)
	if mc == nil {
		return nil, errors.New("harness: approve: no model response before the approval")
	}
	var plan []approvalItem
	for _, tc := range mc.GetToolCalls() {
		item, err := planCall(tc, tail, res)
		if err != nil {
			return nil, err
		}
		plan = append(plan, item)
	}
	return plan, nil
}

func planCall(tc *adlcv1.ToolCall, tail []*adlcv1.Event, res Resolution) (approvalItem, error) {
	args := json.RawMessage(tc.GetArgumentsJson())
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	item := approvalItem{call: model.ToolCall{ID: tc.GetId(), Name: tc.GetName(), Arguments: args}}
	var requested bool
	var aid string
	for _, ev := range tail {
		switch {
		case ev.GetToolCallCompleted().GetCallId() == tc.GetId():
			item.kind = itemDone
			return item, nil
		case ev.GetToolCallRequested().GetCall().GetId() == tc.GetId():
			requested = true
			item.reqSeq = ev.GetSeq()
		case ev.GetApprovalRequested().GetCallId() == tc.GetId():
			aid = ev.GetApprovalRequested().GetApprovalId()
		}
	}
	switch {
	case aid != "":
		return resolveItem(item, tail, aid, res), nil
	case !requested:
		item.kind = itemNotRun
		return item, nil
	}
	return item, fmt.Errorf("harness: approve: tool call %q was requested but has no result and no approval", tc.GetId())
}

// resolveItem sets the kind and the resolution of a call that has an approval.
func resolveItem(item approvalItem, tail []*adlcv1.Event, aid string, res Resolution) approvalItem {
	approved, by, comment := res.Approved, res.By, res.Comment
	if aid != res.ApprovalID {
		for _, ev := range tail {
			if r := ev.GetApprovalResolved(); r.GetApprovalId() == aid {
				approved, by, comment = r.GetApproved(), r.GetResolvedBy(), r.GetComment()
			}
		}
	}
	item.kind, item.by, item.comment = itemRejected, by, comment
	if approved {
		item.kind = itemApproved
	}
	return item
}

// runApproved runs a call whose approval was granted: PreTool hooks, the
// tool and PostTool hooks, but no policy check and no budget check (the call
// was counted when it was requested). It sets l.final when the run stops.
func (l *loop) runApproved(call model.ToolCall, reqSeq uint64) error {
	if l.ctx.Err() != nil {
		return l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "")
	}
	tool, known := l.byName[call.Name]
	if !known {
		return l.addErrorResult(call.ID, reqSeq, l.unknownToolMessage(call.Name))
	}
	if stopped, err := l.runPreHooks(call, tool, reqSeq); stopped || err != nil {
		return err
	}
	return l.execute(call, reqSeq)
}
