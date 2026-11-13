package harness

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// loop holds the state of one Run call.
type loop struct {
	ctx    context.Context // the caller's ctx: cancels model, policy and tool calls
	cfg    Config
	deps   Deps
	r      *recorder
	byName map[string]toolrouter.Tool
	specs  []model.ToolSpec
	final  *eventlog.RunState // set by finish

	startSeq uint64    // seq of the current model_call_started
	t0       time.Time // when the current model call began
}

func newLoop(ctx context.Context, cfg Config, deps Deps) *loop {
	return &loop{ctx: ctx, cfg: cfg, deps: deps, r: newRecorder(ctx, deps.Log, cfg.RunID, cfg.AgentID)}
}

// add records ev; a failure is a failure to use the event log.
func (l *loop) add(ev *adlcv1.Event) (uint64, error) {
	seq, err := l.r.add(ev)
	if err != nil {
		return 0, fmt.Errorf("harness: %w", err)
	}
	return seq, nil
}

// finish records run_completed and keeps a copy of the final state.
func (l *loop) finish(reason adlcv1.StopReason, result, detail string) error {
	_, err := l.add(&adlcv1.Event{Payload: &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{
		StopReason: reason, Result: result, Detail: detail,
	}}})
	if err != nil {
		return err
	}
	st := l.r.state
	l.final = &st
	return nil
}

func (l *loop) run() (*eventlog.RunState, error) {
	if _, err := l.add(&adlcv1.Event{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{
		AgentVersion: l.cfg.AgentVersion, Task: l.cfg.Task, Budget: l.cfg.Budget,
	}}}); err != nil {
		return nil, err
	}
	l.loadTools()
	for step := uint32(1); ; step++ {
		// 0.5.2 adds the budget check here.
		if l.ctx.Err() != nil {
			err := l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "")
			return l.final, err
		}
		if err := l.step(step); err != nil || l.final != nil {
			return l.final, err
		}
	}
}

func (l *loop) loadTools() {
	tools := l.deps.Tools.Tools()
	l.byName = make(map[string]toolrouter.Tool, len(tools))
	for _, t := range tools {
		l.byName[t.Spec.Name] = t
		l.specs = append(l.specs, t.Spec)
	}
}

// step runs one model call and the tool calls it asks for. It sets l.final
// when the run stops.
func (l *loop) step(step uint32) error {
	msgs, err := Transcript(l.r.events, l.cfg.MaxToolOutputBytes)
	if err != nil {
		return err
	}
	req := &model.Request{
		Model: l.cfg.Model, System: l.cfg.System, Messages: msgs, Tools: l.specs,
		MaxTokens: l.cfg.MaxTokens, Effort: l.cfg.Effort,
	}
	callID := fmt.Sprintf("m%d", step)
	resp, err := l.callModel(req, callID, step)
	if err != nil || resp == nil {
		return err
	}
	calls := resp.Message.ToolCalls()
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("%s-t%d", callID, i+1)
		}
	}
	if err := l.recordResponse(callID, resp, calls); err != nil {
		return err
	}
	return l.afterResponse(resp, calls)
}

// callModel records model_call_started and calls the model. It returns a nil
// response, and sets l.final, when the call failed or was cancelled.
func (l *loop) callModel(req *model.Request, callID string, step uint32) (*model.Response, error) {
	startSeq, err := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{
		CallId: callID, Step: step, Provider: l.deps.Model.Provider(), Model: l.cfg.Model, Attempt: 1,
	}}})
	if err != nil {
		return nil, err
	}
	l.startSeq = startSeq
	l.t0 = l.deps.Now()
	resp, err := l.deps.Model.Generate(l.ctx, req)
	if err == nil {
		return resp, nil
	}
	if l.ctx.Err() != nil {
		return nil, l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "")
	}
	class, status := adlcv1.ErrorClass_ERROR_CLASS_INTERNAL, 0
	var me *model.Error
	if errors.As(err, &me) {
		class, status = me.Class, me.HTTPStatus
	}
	if _, err2 := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ModelCallFailed{ModelCallFailed: &adlcv1.ModelCallFailed{
		CallId: callID, ErrorClass: class, Message: err.Error(), WillRetry: false,
		HttpStatus: int32(status), //nolint:gosec // HTTP status fits in int32
	}}}); err2 != nil {
		return nil, err2
	}
	return nil, l.finish(adlcv1.StopReason_STOP_REASON_ERROR, "", err.Error())
}

// recordResponse writes model_call_completed with the cost of the call.
func (l *loop) recordResponse(callID string, resp *model.Response, calls []model.ToolCall) error {
	done := &adlcv1.ModelCallCompleted{
		CallId: callID, FinishReason: resp.Finish, ProviderFinishReason: resp.ProviderFinish,
		Text: resp.Message.Text(), Latency: durationpb.New(l.deps.Now().Sub(l.t0)),
	}
	for _, c := range calls {
		done.ToolCalls = append(done.ToolCalls, &adlcv1.ToolCall{Id: c.ID, Name: c.Name, ArgumentsJson: string(c.Arguments)})
	}
	for i, p := range resp.Message.Parts {
		if p.Kind == model.PartBlob && p.Blob != nil {
			done.ProviderBlobs = append(done.ProviderBlobs, &adlcv1.ProviderBlob{
				Provider: p.Blob.Provider, Kind: p.Blob.Kind, Data: p.Blob.Data,
				Index: uint32(i), //nolint:gosec // part index is small
			})
		}
	}
	_, err := l.add(&adlcv1.Event{
		ParentSeq: l.startSeq, Payload: &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: done},
		Cost: &adlcv1.Cost{
			InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens,
			CacheReadTokens: resp.Usage.CacheReadTokens, CacheWriteTokens: resp.Usage.CacheWriteTokens,
			UsdMicros: l.cfg.Price.Cost(resp.Usage),
		},
	})
	return err
}

// afterResponse decides what a recorded response means: stop, or run its tool calls.
func (l *loop) afterResponse(resp *model.Response, calls []model.ToolCall) error {
	if resp.Finish == adlcv1.FinishReason_FINISH_REASON_REFUSAL {
		return l.finish(adlcv1.StopReason_STOP_REASON_ERROR, "", "model refused")
	}
	if len(calls) == 0 {
		if resp.Finish == adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS {
			return l.finish(adlcv1.StopReason_STOP_REASON_ERROR, "", "output limit reached")
		}
		return l.finish(adlcv1.StopReason_STOP_REASON_COMPLETED, resp.Message.Text(), "")
	}
	for _, call := range calls {
		if err := l.runToolCall(call); err != nil || l.final != nil {
			return err
		}
	}
	// 0.5.4 adds the checkpoint here.
	return nil
}

// runToolCall runs one tool call. It sets l.final when the run stops.
func (l *loop) runToolCall(call model.ToolCall) error {
	if l.ctx.Err() != nil {
		return l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "")
	}
	tool, known := l.byName[call.Name]
	reqSeq, err := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ToolCallRequested{ToolCallRequested: &adlcv1.ToolCallRequested{
		Call:      &adlcv1.ToolCall{Id: call.ID, Name: call.Name, ArgumentsJson: string(call.Arguments)},
		RiskClass: tool.Risk, Server: tool.Server,
	}}})
	if err != nil {
		return err
	}
	if !known {
		_, err := l.add(&adlcv1.Event{ParentSeq: reqSeq, Payload: &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{
			CallId: call.ID, IsError: true, Output: []byte(l.unknownToolMessage(call.Name)),
		}}})
		return err
	}
	// 0.5.4 runs PreTool hooks here.
	if stopped, err := l.authorize(call, tool, reqSeq); stopped || err != nil {
		return err
	}
	res, err := l.callTool(call)
	if err != nil || l.final != nil {
		return err
	}
	_, truncated := toolrouter.ForModel(res.Output, l.cfg.MaxToolOutputBytes)
	_, err = l.add(&adlcv1.Event{ParentSeq: reqSeq, Payload: &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{
		CallId: call.ID, IsError: res.IsError, Output: res.Output, TruncatedForModel: truncated,
		Duration: durationpb.New(res.Duration), ExitCode: res.ExitCode,
	}}})
	// 0.5.4 runs PostTool hooks here.
	return err
}

func (l *loop) unknownToolMessage(name string) string {
	names := make([]string, 0, len(l.byName))
	for n := range l.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return fmt.Sprintf("unknown tool %q; available tools: %s", name, strings.Join(names, ", "))
}

// authorize asks the policy and records the decision. It returns true when
// the call must not run (the run has been finished).
func (l *loop) authorize(call model.ToolCall, tool toolrouter.Tool, reqSeq uint64) (bool, error) {
	dec, err := l.deps.Policy.Authorize(l.ctx, policy.Request{
		AgentID: l.cfg.AgentID, Tool: call.Name, Risk: tool.Risk, ArgumentsJSON: string(call.Arguments),
	})
	if err != nil {
		return true, l.policyError(err)
	}
	if _, err := l.add(&adlcv1.Event{ParentSeq: reqSeq, Payload: &adlcv1.Event_PolicyDecided{PolicyDecided: &adlcv1.PolicyDecided{
		CallId: call.ID, Decision: dec.Decision, RuleId: dec.RuleID, Reason: dec.Reason, Source: dec.Source,
	}}}); err != nil {
		return true, err
	}
	switch dec.Decision {
	case adlcv1.Decision_DECISION_ALLOW:
		return false, nil
	case adlcv1.Decision_DECISION_DENY:
		return true, l.finish(adlcv1.StopReason_STOP_REASON_DENIED, "",
			fmt.Sprintf("rule %s: %s", dec.RuleID, dec.Reason))
	case adlcv1.Decision_DECISION_NEEDS_APPROVAL:
		if _, err := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ApprovalRequested{ApprovalRequested: &adlcv1.ApprovalRequested{
			ApprovalId: "a-" + call.ID, CallId: call.ID, Summary: call.Name + " " + string(call.Arguments),
		}}}); err != nil {
			return true, err
		}
		return true, l.finish(adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL, "", "")
	default:
		return true, l.finish(adlcv1.StopReason_STOP_REASON_ERROR, "", "policy returned no decision")
	}
}

func (l *loop) policyError(err error) error {
	if l.ctx.Err() != nil {
		return l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "")
	}
	if _, err2 := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{
		ErrorClass: adlcv1.ErrorClass_ERROR_CLASS_POLICY, Message: err.Error(), Component: "policy",
	}}}); err2 != nil {
		return err2
	}
	return l.finish(adlcv1.StopReason_STOP_REASON_ERROR, "", "policy: "+err.Error())
}

// callTool runs the tool. A router error becomes an error result the model
// sees; cancellation finishes the run instead (l.final is set).
func (l *loop) callTool(call model.ToolCall) (toolrouter.Result, error) {
	t0 := l.deps.Now()
	res, err := l.deps.Tools.Call(l.ctx, call)
	res.Duration = l.deps.Now().Sub(t0)
	if err == nil {
		return res, nil
	}
	if l.ctx.Err() != nil {
		return res, l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "")
	}
	if _, err2 := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{
		ErrorClass: adlcv1.ErrorClass_ERROR_CLASS_TOOL, Message: err.Error(), Component: "toolrouter",
	}}}); err2 != nil {
		return res, err2
	}
	return toolrouter.Result{
		Output: []byte("tool call failed: " + err.Error()), IsError: true, Duration: res.Duration,
	}, nil
}
