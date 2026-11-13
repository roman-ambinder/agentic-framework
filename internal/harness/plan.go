package harness

import (
	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// runPlanCall makes the planning call (task Q.4): one model call without
// tools, recorded as call id PlanCallID, step 0. It sets l.final when the run
// stops. (approve.go already has runPlan, for approvals.)
func (l *loop) runPlanCall() error {
	if l.ctx.Err() != nil {
		return l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "")
	}
	if stopped, err := l.checkKill(); stopped || err != nil {
		return err
	}
	if e := beforeModelCall(l.cfg.Budget, l.usage()); e != nil {
		return l.finishBudget(e)
	}
	req, err := l.compact(l.planRequest())
	if err != nil || l.final != nil {
		return err
	}
	resp, err := l.callModel(req, PlanCallID, 0)
	if err != nil || resp == nil {
		return err
	}
	// Tool calls in a plan response are dropped: the request offered no tools.
	if err := l.recordResponse(PlanCallID, resp, nil); err != nil {
		return err
	}
	if resp.Finish == adlcv1.FinishReason_FINISH_REASON_REFUSAL {
		return l.finish(adlcv1.StopReason_STOP_REASON_ERROR, "", "model refused")
	}
	return nil
}

// planRequest builds the request of the planning call: the task, then the
// plan prompt, and no tools. It reads the task from the log, so Resume works.
func (l *loop) planRequest() *model.Request {
	return &model.Request{
		Model: l.cfg.Model, System: l.cfg.System, MaxTokens: l.cfg.Plan.MaxTokens, Effort: l.cfg.Effort,
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{
			model.TextPart(l.r.state.Task), model.TextPart(l.cfg.Plan.Prompt),
		}}},
	}
}

// hasPlan reports whether the events hold a completed planning call.
func hasPlan(events []*adlcv1.Event) bool {
	for _, ev := range events {
		if ev.GetModelCallCompleted().GetCallId() == PlanCallID {
			return true
		}
	}
	return false
}
