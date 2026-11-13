package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

const (
	stopCompleted = adlcv1.StopReason_STOP_REASON_COMPLETED
	stopError     = adlcv1.StopReason_STOP_REASON_ERROR
)

func defaultDeps(t *testing.T) Deps {
	t.Helper()
	return Deps{Tools: staticTools(t), Policy: policy.AllowAll{}}
}

func wantKinds(t *testing.T, got []*adlcv1.Event, want ...string) {
	t.Helper()
	if k := kinds(got); !reflect.DeepEqual(k, want) {
		t.Fatalf("kinds = %v\nwant    %v", k, want)
	}
}

func wantStop(t *testing.T, st *eventlog.RunState, reason adlcv1.StopReason, detail string) {
	t.Helper()
	if st.StopReason != reason || st.StopDetail != detail {
		t.Fatalf("stop = %s %q, want %s %q", st.StopReason, st.StopDetail, reason, detail)
	}
}

func TestRunCompletesWithoutTools(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t), textTurn("all done"))
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_completed", "run_completed")
	wantStop(t, r.state, stopCompleted, "")
	if r.state.Result != "all done" {
		t.Fatalf("result = %q", r.state.Result)
	}
	if r.state.Status != eventlog.StatusFinished {
		t.Fatalf("status = %s", r.state.Status)
	}
	started := r.events[1].GetModelCallStarted()
	if started.GetCallId() != "m1" || started.GetStep() != 1 || started.GetAttempt() != 1 ||
		started.GetProvider() != "fake" || started.GetModel() != "m" {
		t.Fatalf("model_call_started = %v", started)
	}
	if r.events[2].GetParentSeq() != r.events[1].GetSeq() {
		t.Fatal("model_call_completed parent_seq is not the model_call_started seq")
	}
	for _, ev := range r.events {
		if ev.GetRunId() != "r1" || ev.GetAgentId() != "test@1" {
			t.Fatalf("event %d has run %q agent %q", ev.GetSeq(), ev.GetRunId(), ev.GetAgentId())
		}
	}
	req := r.model.Requests()[0]
	if req.MaxTokens != 4096 || req.Model != "m" || len(req.Tools) != 1 {
		t.Fatalf("request = %+v", req)
	}
}

func TestRunExecutesToolCallAndContinues(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(readCall("t1", "a.go")), textTurn("ok"))
	wantKinds(t, r.events, "run_started",
		"model_call_started", "model_call_completed",
		"tool_call_requested", "policy_decided", "tool_call_completed", "checkpoint",
		"model_call_started", "model_call_completed", "run_completed")
	wantStop(t, r.state, stopCompleted, "")
	if r.state.ToolCalls != 1 || r.state.Step != 2 {
		t.Fatalf("state = %+v", r.state)
	}
	req := r.model.Requests()[1]
	if len(req.Messages) != 3 {
		t.Fatalf("2nd request has %d messages, want 3", len(req.Messages))
	}
	call := req.Messages[1].Parts[0].ToolCall
	if req.Messages[1].Role != model.RoleAssistant || call == nil || call.ID != "t1" {
		t.Fatalf("assistant message = %+v", req.Messages[1])
	}
	res := req.Messages[2].Parts[0].ToolResult
	if req.Messages[2].Role != model.RoleUser || res.CallID != "t1" || res.Content != "contents of a.go" || res.IsError {
		t.Fatalf("tool result message = %+v", req.Messages[2])
	}
	pd := r.events[4].GetPolicyDecided()
	if pd.GetCallId() != "t1" || pd.GetDecision() != adlcv1.Decision_DECISION_ALLOW || r.events[4].GetParentSeq() != r.events[3].GetSeq() {
		t.Fatalf("policy_decided = %v parent %d", pd, r.events[4].GetParentSeq())
	}
	tr := r.events[3].GetToolCallRequested()
	if tr.GetRiskClass() != adlcv1.RiskClass_RISK_CLASS_READ || tr.GetServer() != "fs" {
		t.Fatalf("tool_call_requested = %v", tr)
	}
}

func TestRunRunsParallelCallsInOrder(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(readCall("t1", "a"), readCall("t2", "b")), textTurn("ok"))
	done := pick(r.events, (*adlcv1.Event).GetToolCallCompleted)
	if len(done) != 2 || done[0].GetCallId() != "t1" || done[1].GetCallId() != "t2" {
		t.Fatalf("completed = %v", done)
	}
	last := r.model.Requests()[1].Messages[2]
	if len(last.Parts) != 2 || last.Parts[0].ToolResult.CallID != "t1" || last.Parts[1].ToolResult.CallID != "t2" ||
		last.Parts[1].ToolResult.Content != "contents of b" {
		t.Fatalf("results = %+v", last.Parts)
	}
}

func TestRunUnknownToolIsFedBack(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(model.ToolCall{ID: "t1", Name: "nope__x", Arguments: json.RawMessage(`{}`)}), textTurn("ok"))
	wantStop(t, r.state, stopCompleted, "")
	if n := len(pick(r.events, (*adlcv1.Event).GetPolicyDecided)); n != 0 {
		t.Fatalf("%d policy_decided events for an unknown tool", n)
	}
	req := pick(r.events, (*adlcv1.Event).GetToolCallRequested)[0]
	if req.GetRiskClass() != adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED || req.GetServer() != "" {
		t.Fatalf("tool_call_requested = %v", req)
	}
	done := pick(r.events, (*adlcv1.Event).GetToolCallCompleted)[0]
	want := `unknown tool "nope__x"; available tools: fs__read_file`
	if !done.GetIsError() || string(done.GetOutput()) != want {
		t.Fatalf("completed = %v, want error %q", done, want)
	}
	if res := r.model.Requests()[1].Messages[2].Parts[0].ToolResult; !res.IsError || res.Content != want {
		t.Fatalf("model saw %+v", res)
	}
}

func TestRunToolErrorResultIsFedBack(t *testing.T) {
	tool := funcTool("x__fail", adlcv1.RiskClass_RISK_CLASS_READ, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		return toolrouter.Result{Output: []byte("boom"), IsError: true, ExitCode: 2}, nil
	})
	deps := Deps{Tools: staticTools(t, tool), Policy: policy.AllowAll{}}
	r := runHarness(t, context.Background(), baseConfig(), deps,
		toolTurn(model.ToolCall{ID: "t1", Name: "x__fail"}), textTurn("ok"))
	wantStop(t, r.state, stopCompleted, "")
	if res := r.model.Requests()[1].Messages[2].Parts[0].ToolResult; !res.IsError || res.Content != "boom" {
		t.Fatalf("model saw %+v", res)
	}
	done := pick(r.events, (*adlcv1.Event).GetToolCallCompleted)[0]
	if done.GetExitCode() != 2 || r.state.ToolErrors != 1 {
		t.Fatalf("completed = %v, state = %+v", done, r.state)
	}
}

func TestRunRouterErrorIsFedBack(t *testing.T) {
	tool := funcTool("x__crash", adlcv1.RiskClass_RISK_CLASS_READ, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		return toolrouter.Result{}, errors.New("server crashed")
	})
	deps := Deps{Tools: staticTools(t, tool), Policy: policy.AllowAll{}}
	r := runHarness(t, context.Background(), baseConfig(), deps,
		toolTurn(model.ToolCall{ID: "t1", Name: "x__crash"}), textTurn("ok"))
	wantStop(t, r.state, stopCompleted, "")
	errs := pick(r.events, (*adlcv1.Event).GetErrorOccurred)
	if len(errs) != 1 || errs[0].GetComponent() != "toolrouter" || errs[0].GetErrorClass() != adlcv1.ErrorClass_ERROR_CLASS_TOOL {
		t.Fatalf("errors = %v", errs)
	}
	res := r.model.Requests()[1].Messages[2].Parts[0].ToolResult
	if !res.IsError || !strings.HasPrefix(res.Content, "tool call failed: ") || !strings.Contains(res.Content, "server crashed") {
		t.Fatalf("model saw %+v", res)
	}
}

func TestRunPolicyDeny(t *testing.T) {
	called := false
	tool := funcTool("x__read", adlcv1.RiskClass_RISK_CLASS_READ, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		called = true
		return toolrouter.Result{}, nil
	})
	pol, err := policy.NewStatic([]policy.Rule{{ID: "no-read", Risk: adlcv1.RiskClass_RISK_CLASS_READ, Decision: adlcv1.Decision_DECISION_DENY}},
		adlcv1.Decision_DECISION_ALLOW)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Tools: staticTools(t, tool), Policy: pol}
	r := runHarness(t, context.Background(), baseConfig(), deps,
		toolTurn(model.ToolCall{ID: "t1", Name: "x__read"}, readCall("t2", "a")), textTurn("unused"))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_DENIED, "rule no-read: matched rule no-read")
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_completed",
		"tool_call_requested", "policy_decided", "run_completed")
	if called || r.state.Denials != 1 || r.model.Remaining() != 1 {
		t.Fatalf("called=%v state=%+v remaining=%d", called, r.state, r.model.Remaining())
	}
}

func TestRunPolicyNeedsApproval(t *testing.T) {
	called := false
	tool := funcTool("x__w", adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		called = true
		return toolrouter.Result{}, nil
	})
	pol, err := policy.NewStatic(nil, adlcv1.Decision_DECISION_NEEDS_APPROVAL)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Tools: staticTools(t, tool), Policy: pol}
	r := runHarness(t, context.Background(), baseConfig(), deps,
		toolTurn(model.ToolCall{ID: "t1", Name: "x__w", Arguments: json.RawMessage(`{"a":1}`)}))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL, "")
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_completed",
		"tool_call_requested", "policy_decided", "approval_requested", "run_completed")
	ap := r.events[5].GetApprovalRequested()
	if ap.GetApprovalId() != "a-t1" || ap.GetCallId() != "t1" || ap.GetSummary() != `x__w {"a":1}` {
		t.Fatalf("approval_requested = %v", ap)
	}
	if r.state.Status != eventlog.StatusPaused || called {
		t.Fatalf("status = %s, called = %v", r.state.Status, called)
	}
}

type errPolicy struct{}

func (errPolicy) Authorize(context.Context, policy.Request) (policy.Decision, error) {
	return policy.Decision{}, errors.New("policy server down")
}

type emptyPolicy struct{}

func (emptyPolicy) Authorize(context.Context, policy.Request) (policy.Decision, error) {
	return policy.Decision{}, nil
}

func TestRunPolicyError(t *testing.T) {
	deps := Deps{Tools: staticTools(t), Policy: errPolicy{}}
	r := runHarness(t, context.Background(), baseConfig(), deps, toolTurn(readCall("t1", "a")))
	wantStop(t, r.state, stopError, "policy: policy server down")
	errs := pick(r.events, (*adlcv1.Event).GetErrorOccurred)
	if len(errs) != 1 || errs[0].GetComponent() != "policy" || errs[0].GetErrorClass() != adlcv1.ErrorClass_ERROR_CLASS_POLICY {
		t.Fatalf("errors = %v", errs)
	}
}

func TestRunPolicyWithoutDecision(t *testing.T) {
	deps := Deps{Tools: staticTools(t), Policy: emptyPolicy{}}
	r := runHarness(t, context.Background(), baseConfig(), deps, toolTurn(readCall("t1", "a")))
	wantStop(t, r.state, stopError, "policy returned no decision")
}

func respWithFinish(turn model.FakeTurn, f adlcv1.FinishReason) model.FakeTurn {
	turn.Response.Finish = f
	return turn
}

func TestRunModelRefusal(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		respWithFinish(textTurn("no"), adlcv1.FinishReason_FINISH_REASON_REFUSAL))
	wantStop(t, r.state, stopError, "model refused")
}

func TestRunMaxTokensWithoutToolCalls(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		respWithFinish(textTurn("cut of"), adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS))
	wantStop(t, r.state, stopError, "output limit reached")
}

func TestRunModelError(t *testing.T) {
	merr := &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, HTTPStatus: 429, Retryable: true, Err: errors.New("slow down")}
	cfg := baseConfig()
	cfg.Retry.MaxAttempts = 1
	r := runHarness(t, context.Background(), cfg, defaultDeps(t), model.FakeTurn{Err: merr})
	wantStop(t, r.state, stopError, merr.Error())
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_failed", "run_completed")
	f := r.events[2].GetModelCallFailed()
	if f.GetCallId() != "m1" || f.GetErrorClass() != adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT ||
		f.GetHttpStatus() != 429 || f.GetWillRetry() || f.GetMessage() != merr.Error() {
		t.Fatalf("model_call_failed = %v", f)
	}
}

func TestRunPlainModelErrorIsInternal(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t), model.FakeTurn{Err: errors.New("weird")})
	f := r.events[2].GetModelCallFailed()
	if f.GetErrorClass() != adlcv1.ErrorClass_ERROR_CLASS_INTERNAL || f.GetHttpStatus() != 0 {
		t.Fatalf("model_call_failed = %v", f)
	}
}

func TestRunScriptExhausted(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t), toolTurn(readCall("t1", "a")))
	if r.state.StopReason != stopError || !strings.Contains(r.state.StopDetail, "script exhausted") {
		t.Fatalf("stop = %s %q", r.state.StopReason, r.state.StopDetail)
	}
}

func TestRunCancelledBeforeFirstCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := runHarness(t, ctx, baseConfig(), defaultDeps(t), textTurn("unused"))
	wantKinds(t, r.events, "run_started", "run_completed")
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_CANCELLED, "")
}

func TestRunCancelledDuringToolCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := funcTool("x__slow", adlcv1.RiskClass_RISK_CLASS_READ, func(ctx context.Context, _ json.RawMessage) (toolrouter.Result, error) {
		cancel()
		return toolrouter.Result{}, ctx.Err()
	})
	deps := Deps{Tools: staticTools(t, tool), Policy: policy.AllowAll{}}
	r := runHarness(t, ctx, baseConfig(), deps, toolTurn(model.ToolCall{ID: "t1", Name: "x__slow"}, readCall("t2", "a")))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_CANCELLED, "")
	if k := kinds(r.events); k[len(k)-1] != "run_completed" {
		t.Fatalf("kinds = %v", k)
	}
	if n := len(pick(r.events, (*adlcv1.Event).GetToolCallRequested)); n != 1 {
		t.Fatalf("%d calls requested, want 1 (later calls must not start)", n)
	}
}

// ticker is a clock that moves 1s on every call.
type ticker struct{ t time.Time }

func (c *ticker) Now() time.Time {
	c.t = c.t.Add(time.Second)
	return c.t
}

func TestRunRecordsCostAndLatency(t *testing.T) {
	cfg := baseConfig()
	cfg.Price = model.Price{InputPerMTok: 1_000_000}
	turn := textTurn("ok")
	turn.Response.Usage = model.Usage{InputTokens: 10, OutputTokens: 3, CacheReadTokens: 2, CacheWriteTokens: 1}
	deps := defaultDeps(t)
	deps.Now = (&ticker{}).Now
	r := runHarness(t, context.Background(), cfg, deps, turn)
	done := r.events[2]
	c := done.GetCost()
	if c.GetUsdMicros() != 10 || c.GetInputTokens() != 10 || c.GetOutputTokens() != 3 ||
		c.GetCacheReadTokens() != 2 || c.GetCacheWriteTokens() != 1 {
		t.Fatalf("cost = %v", c)
	}
	if lat := done.GetModelCallCompleted().GetLatency().AsDuration(); lat != time.Second {
		t.Fatalf("latency = %s", lat)
	}
	if r.state.Usage.USDMicros != 10 || r.state.Usage.InputTokens != 10 {
		t.Fatalf("usage = %+v", r.state.Usage)
	}
}

func TestRunRecordsToolDuration(t *testing.T) {
	deps := defaultDeps(t)
	deps.Now = (&ticker{}).Now
	r := runHarness(t, context.Background(), baseConfig(), deps, toolTurn(readCall("t1", "a")), textTurn("ok"))
	done := pick(r.events, (*adlcv1.Event).GetToolCallCompleted)[0]
	if d := done.GetDuration().AsDuration(); d != time.Second {
		t.Fatalf("duration = %s", d)
	}
}

func TestRunAssignsMissingToolCallIDs(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(readCall("", "a"), readCall("", "b")), textTurn("ok"))
	calls := pick(r.events, (*adlcv1.Event).GetModelCallCompleted)[0].GetToolCalls()
	reqs := pick(r.events, (*adlcv1.Event).GetToolCallRequested)
	for i, want := range []string{"m1-t1", "m1-t2"} {
		if calls[i].GetId() != want || reqs[i].GetCall().GetId() != want {
			t.Fatalf("call %d ids: model_call_completed %q, requested %q, want %q", i, calls[i].GetId(), reqs[i].GetCall().GetId(), want)
		}
	}
	if id := r.model.Requests()[1].Messages[1].Parts[0].ToolCall.ID; id != "m1-t1" {
		t.Fatalf("id in next request = %q", id)
	}
}

func TestRunStoresProviderBlobs(t *testing.T) {
	blob := model.Blob{Provider: "fake", Kind: "thinking", Data: []byte("zzz")}
	turn := model.FakeTurn{Response: &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{
			model.BlobPart(blob), model.TextPart("hm"), model.ToolCallPart(readCall("t1", "a")), model.BlobPart(blob),
		}},
		Finish: adlcv1.FinishReason_FINISH_REASON_TOOL_USE,
	}}
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t), turn, textTurn("ok"))
	blobs := pick(r.events, (*adlcv1.Event).GetModelCallCompleted)[0].GetProviderBlobs()
	if len(blobs) != 2 || blobs[1].GetIndex() != 3 || blobs[0].GetIndex() != 0 || blobs[0].GetKind() != "thinking" || string(blobs[0].GetData()) != "zzz" {
		t.Fatalf("blobs = %v", blobs)
	}
	parts := r.model.Requests()[1].Messages[1].Parts
	if len(parts) != 4 || parts[3].Kind != model.PartBlob || parts[0].Kind != model.PartBlob || !reflect.DeepEqual(*parts[0].Blob, blob) ||
		parts[1].Kind != model.PartText || parts[2].Kind != model.PartToolCall {
		t.Fatalf("assistant parts = %+v", parts)
	}
}

func TestRunCapsToolOutputForModel(t *testing.T) {
	big := strings.Repeat("y", 50)
	tool := funcTool("x__big", adlcv1.RiskClass_RISK_CLASS_READ, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		return toolrouter.Result{Output: []byte(big)}, nil
	})
	cfg := baseConfig()
	cfg.MaxToolOutputBytes = 10
	deps := Deps{Tools: staticTools(t, tool), Policy: policy.AllowAll{}}
	r := runHarness(t, context.Background(), cfg, deps, toolTurn(model.ToolCall{ID: "t1", Name: "x__big"}), textTurn("ok"))
	done := pick(r.events, (*adlcv1.Event).GetToolCallCompleted)[0]
	if string(done.GetOutput()) != big || !done.GetTruncatedForModel() {
		t.Fatalf("completed = %v", done)
	}
	want, _ := toolrouter.ForModel([]byte(big), 10)
	if got := r.model.Requests()[1].Messages[2].Parts[0].ToolResult.Content; got != want {
		t.Fatalf("model saw %q, want %q", got, want)
	}
}

func TestRunInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config, *Deps)
		want   string
	}{
		{"run id", func(c *Config, _ *Deps) { c.RunID = "" }, "harness: config: run id is empty"},
		{"task", func(c *Config, _ *Deps) { c.Task = "" }, "harness: config: task is empty"},
		{"log", func(_ *Config, d *Deps) { d.Log = nil }, "harness: deps: log is nil"},
		{"model", func(_ *Config, d *Deps) { d.Model = nil }, "harness: deps: model is nil"},
		{"tools", func(_ *Config, d *Deps) { d.Tools = nil }, "harness: deps: tools is nil"},
		{"policy", func(_ *Config, d *Deps) { d.Policy = nil }, "harness: deps: policy is nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newLog(t)
			cfg, deps := baseConfig(), defaultDeps(t)
			deps.Log, deps.Model = store, model.NewFake(textTurn("x"))
			tt.mutate(&cfg, &deps)
			state, err := Run(context.Background(), cfg, deps)
			if state != nil || err == nil || err.Error() != tt.want {
				t.Fatalf("Run = %v, %v; want error %q", state, err, tt.want)
			}
			if evs, _ := store.Read(context.Background(), "r1", 0); len(evs) != 0 {
				t.Fatalf("%d events written for an invalid config", len(evs))
			}
		})
	}
}

func TestRunLogFailure(t *testing.T) {
	deps := defaultDeps(t)
	deps.Log = &failingLog{Log: newLog(t), n: 1}
	deps.Model = model.NewFake(textTurn("x"))
	state, err := Run(context.Background(), baseConfig(), deps)
	if state != nil || !errors.Is(err, errLogDown) || !strings.HasPrefix(err.Error(), "harness: ") {
		t.Fatalf("Run = %v, %v", state, err)
	}
}

func TestRecorderAddFillsIdsAndState(t *testing.T) {
	store := newLog(t)
	rec := newRecorder(context.Background(), store, "r9", "a@1")
	seq, err := rec.add(&adlcv1.Event{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{Task: "t"}}})
	if err != nil || seq != 1 {
		t.Fatalf("seq = %d, err = %v", seq, err)
	}
	ev := rec.events[0]
	if ev.GetRunId() != "r9" || ev.GetAgentId() != "a@1" || ev.GetTime() == nil {
		t.Fatalf("event = %v", ev)
	}
	if rec.state.RunID != "r9" || rec.state.Task != "t" || rec.state.LastSeq != 1 {
		t.Fatalf("state = %+v", rec.state)
	}
}

func TestRecorderAddRejectsInvalidSequence(t *testing.T) {
	rec := newRecorder(context.Background(), newLog(t), "r9", "a@1")
	_, _ = rec.add(&adlcv1.Event{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{Task: "t"}}})
	_, err := rec.add(&adlcv1.Event{Payload: &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{CallId: "nope"}}})
	if !errors.Is(err, eventlog.ErrInvalidLog) {
		t.Fatalf("err = %v, want ErrInvalidLog", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("events = %d, want 1", len(rec.events))
	}
}

func TestSleepCtxReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

// cancelAfterModel cancels the run's context right after the model answers.
type cancelAfterModel struct {
	*model.Fake
	cancel context.CancelFunc
}

func (m cancelAfterModel) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	resp, err := m.Fake.Generate(ctx, req)
	m.cancel()
	return resp, err
}

func TestRunCancelledAfterModelResponseRequestsNoTool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newLog(t)
	deps := defaultDeps(t)
	deps.Log = store
	deps.Model = cancelAfterModel{Fake: model.NewFake(toolTurn(readCall("t1", "a"))), cancel: cancel}
	state, err := Run(ctx, baseConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	events := assertFolds(t, store, "r1", state)
	wantKinds(t, events, "run_started", "model_call_started", "model_call_completed", "run_completed")
	wantStop(t, state, adlcv1.StopReason_STOP_REASON_CANCELLED, "")
}

const (
	budgetSteps     = adlcv1.BudgetKind_BUDGET_KIND_STEPS
	budgetTokens    = adlcv1.BudgetKind_BUDGET_KIND_TOKENS
	budgetCost      = adlcv1.BudgetKind_BUDGET_KIND_COST
	budgetToolCalls = adlcv1.BudgetKind_BUDGET_KIND_TOOL_CALLS
	budgetWall      = adlcv1.BudgetKind_BUDGET_KIND_WALL_CLOCK
)

func wantBudgetStop(t *testing.T, st *eventlog.RunState, kind adlcv1.BudgetKind, detail string) {
	t.Helper()
	wantStop(t, st, adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED, detail)
	if st.ExhaustedBudget != kind {
		t.Fatalf("exhausted budget = %s, want %s", st.ExhaustedBudget, kind)
	}
}

func countModelCalls(evs []*adlcv1.Event) int {
	return len(pick(evs, (*adlcv1.Event).GetModelCallCompleted))
}

// toolTurns returns n turns that each call fs__read_file once.
func toolTurns(n int, usage model.Usage) []model.FakeTurn {
	turns := make([]model.FakeTurn, n)
	for i := range turns {
		turns[i] = toolTurn(readCall(fmt.Sprintf("t%d", i+1), "a"))
		turns[i].Response.Usage = usage
	}
	return turns
}

func TestRunStopsAtMaxSteps(t *testing.T) {
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxSteps: 2}
	r := runHarness(t, context.Background(), cfg, defaultDeps(t), toolTurns(5, model.Usage{})...)
	if n := countModelCalls(r.events); n != 2 {
		t.Fatalf("%d model calls, want 2", n)
	}
	wantBudgetStop(t, r.state, budgetSteps, "max_steps 2 reached")
}

func TestRunStopsAtMaxTokens(t *testing.T) {
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxTokens: 100}
	r := runHarness(t, context.Background(), cfg, defaultDeps(t),
		toolTurns(5, model.Usage{InputTokens: 40, OutputTokens: 20})...)
	if n := countModelCalls(r.events); n != 2 {
		t.Fatalf("%d model calls, want 2", n)
	}
	wantBudgetStop(t, r.state, budgetTokens, "max_tokens 100 reached (used 120)")
}

func TestRunStopsAtMaxCost(t *testing.T) {
	cfg := baseConfig()
	cfg.Price = model.Price{InputPerMTok: 6_000_000}
	cfg.Budget = &adlcv1.Budget{MaxUsdMicros: 10}
	r := runHarness(t, context.Background(), cfg, defaultDeps(t), toolTurns(5, model.Usage{InputTokens: 1})...)
	if n := countModelCalls(r.events); n != 2 {
		t.Fatalf("%d model calls, want 2", n)
	}
	wantBudgetStop(t, r.state, budgetCost, "max_usd_micros 10 reached (used 12)")
}

func TestRunStopsAtMaxToolCalls(t *testing.T) {
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxToolCalls: 2}
	r := runHarness(t, context.Background(), cfg, defaultDeps(t),
		toolTurn(readCall("t1", "a"), readCall("t2", "b"), readCall("t3", "c")), textTurn("unused"))
	if n := len(pick(r.events, (*adlcv1.Event).GetToolCallRequested)); n != 2 {
		t.Fatalf("%d tool calls requested, want 2", n)
	}
	wantBudgetStop(t, r.state, budgetToolCalls, "max_tool_calls 2 reached")
}

// stepClock is a clock the test moves by hand.
type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time { return c.t }

func TestRunStopsAtWallClockBeforeCall(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	slow := funcTool("x__slow", adlcv1.RiskClass_RISK_CLASS_READ, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		clock.t = clock.t.Add(time.Minute)
		return toolrouter.Result{Output: []byte("ok")}, nil
	})
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxWallClock: durationpb.New(90 * time.Second)}
	deps := Deps{Tools: staticTools(t, slow), Policy: policy.AllowAll{}, Now: clock.Now}
	call := model.ToolCall{Name: "x__slow"}
	r := runHarness(t, context.Background(), cfg, deps,
		toolTurn(call), toolTurn(call), toolTurn(call), textTurn("unused"))
	if n := countModelCalls(r.events); n != 2 {
		t.Fatalf("%d model calls, want 2", n)
	}
	wantBudgetStop(t, r.state, budgetWall, "max_wall_clock 1m30s reached")
}

// blockingTool blocks until its context ends. onStart runs first if set.
func blockingTool(onStart func()) toolrouter.StaticTool {
	return funcTool("x__block", adlcv1.RiskClass_RISK_CLASS_READ, func(ctx context.Context, _ json.RawMessage) (toolrouter.Result, error) {
		if onStart != nil {
			onStart()
		}
		<-ctx.Done()
		return toolrouter.Result{}, ctx.Err()
	})
}

func TestRunWallClockInterruptsTool(t *testing.T) {
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxWallClock: durationpb.New(50 * time.Millisecond)}
	deps := Deps{Tools: staticTools(t, blockingTool(nil)), Policy: policy.AllowAll{}}
	start := time.Now()
	r := runHarness(t, context.Background(), cfg, deps, toolTurn(model.ToolCall{ID: "t1", Name: "x__block"}))
	if time.Since(start) > 2*time.Second {
		t.Fatal("run took longer than 2s")
	}
	wantBudgetStop(t, r.state, budgetWall, "max_wall_clock 50ms reached")
}

// blockingModel blocks in Generate until its context ends.
type blockingModel struct{ *model.Fake }

func (blockingModel) Generate(ctx context.Context, _ *model.Request) (*model.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunWallClockInterruptsModelCall(t *testing.T) {
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxWallClock: durationpb.New(50 * time.Millisecond)}
	store := newLog(t)
	deps := defaultDeps(t)
	deps.Log, deps.Model = store, blockingModel{model.NewFake()}
	state, err := Run(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	assertFolds(t, store, "r1", state)
	wantBudgetStop(t, state, budgetWall, "max_wall_clock 50ms reached")
}

func TestRunCallerCancelIsNotBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxWallClock: durationpb.New(time.Hour)}
	deps := Deps{Tools: staticTools(t, blockingTool(cancel)), Policy: policy.AllowAll{}}
	r := runHarness(t, ctx, cfg, deps, toolTurn(model.ToolCall{ID: "t1", Name: "x__block"}))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_CANCELLED, "")
	if r.state.ExhaustedBudget != adlcv1.BudgetKind_BUDGET_KIND_UNSPECIFIED {
		t.Fatalf("exhausted budget = %s", r.state.ExhaustedBudget)
	}
}

func TestRunNoBudget(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(readCall("t1", "a")), toolTurn(readCall("t2", "b")), textTurn("done"))
	wantStop(t, r.state, stopCompleted, "")
}

func rateLimited() model.FakeTurn {
	return model.FakeTurn{Err: &model.Error{
		Class: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, HTTPStatus: 429, Retryable: true, Err: errors.New("slow down"),
	}}
}

// sleepRecorder replaces Deps.Sleep and records the requested waits.
type sleepRecorder struct{ waits []time.Duration }

func (s *sleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return ctx.Err()
}

func retryDeps(t *testing.T) (Deps, *sleepRecorder) {
	t.Helper()
	rec := &sleepRecorder{}
	deps := defaultDeps(t)
	deps.Sleep = rec.sleep
	return deps, rec
}

func wantWaits(t *testing.T, rec *sleepRecorder, want ...time.Duration) {
	t.Helper()
	if !reflect.DeepEqual(rec.waits, want) {
		t.Fatalf("sleeps = %v, want %v", rec.waits, want)
	}
}

func attempts(evs []*adlcv1.Event) []uint32 {
	var out []uint32
	for _, s := range pick(evs, (*adlcv1.Event).GetModelCallStarted) {
		out = append(out, s.GetAttempt())
	}
	return out
}

func willRetry(evs []*adlcv1.Event) []bool {
	var out []bool
	for _, f := range pick(evs, (*adlcv1.Event).GetModelCallFailed) {
		out = append(out, f.GetWillRetry())
	}
	return out
}

func TestRunRetriesRetryableError(t *testing.T) {
	deps, rec := retryDeps(t)
	r := runHarness(t, context.Background(), baseConfig(), deps, rateLimited(), rateLimited(), textTurn("ok"))
	wantKinds(t, r.events, "run_started",
		"model_call_started", "model_call_failed",
		"model_call_started", "model_call_failed",
		"model_call_started", "model_call_completed", "run_completed")
	wantStop(t, r.state, stopCompleted, "")
	if got := attempts(r.events); !reflect.DeepEqual(got, []uint32{1, 2, 3}) {
		t.Fatalf("attempts = %v", got)
	}
	if got := willRetry(r.events); !reflect.DeepEqual(got, []bool{true, true}) {
		t.Fatalf("will_retry = %v", got)
	}
	for _, s := range pick(r.events, (*adlcv1.Event).GetModelCallStarted) {
		if s.GetCallId() != "m1" || s.GetStep() != 1 {
			t.Fatalf("model_call_started = %v", s)
		}
	}
	wantWaits(t, rec, time.Second, 2*time.Second)
}

func TestRunGivesUpAfterMaxAttempts(t *testing.T) {
	deps, rec := retryDeps(t)
	r := runHarness(t, context.Background(), baseConfig(), deps, rateLimited(), rateLimited(), rateLimited(), textTurn("unused"))
	if got := willRetry(r.events); !reflect.DeepEqual(got, []bool{true, true, false}) {
		t.Fatalf("will_retry = %v", got)
	}
	if r.state.StopReason != stopError {
		t.Fatalf("stop = %s", r.state.StopReason)
	}
	wantWaits(t, rec, time.Second, 2*time.Second)
}

func TestRunDoesNotRetryNonRetryable(t *testing.T) {
	deps, rec := retryDeps(t)
	bad := model.FakeTurn{Err: &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_MODEL, HTTPStatus: 400, Err: errors.New("bad")}}
	r := runHarness(t, context.Background(), baseConfig(), deps, bad, textTurn("unused"))
	if got := attempts(r.events); len(got) != 1 {
		t.Fatalf("attempts = %v", got)
	}
	if got := willRetry(r.events); !reflect.DeepEqual(got, []bool{false}) {
		t.Fatalf("will_retry = %v", got)
	}
	if r.state.StopReason != stopError {
		t.Fatalf("stop = %s", r.state.StopReason)
	}
	wantWaits(t, rec)
}

func TestRunNoRetriesWhenMaxAttemptsOne(t *testing.T) {
	deps, rec := retryDeps(t)
	cfg := baseConfig()
	cfg.Retry.MaxAttempts = 1
	r := runHarness(t, context.Background(), cfg, deps, rateLimited(), textTurn("unused"))
	if got := attempts(r.events); len(got) != 1 {
		t.Fatalf("attempts = %v", got)
	}
	if r.state.StopReason != stopError {
		t.Fatalf("stop = %s", r.state.StopReason)
	}
	wantWaits(t, rec)
}

func TestRunCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := defaultDeps(t)
	deps.Sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	r := runHarness(t, ctx, baseConfig(), deps, rateLimited(), textTurn("unused"))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_CANCELLED, "")
	if got := attempts(r.events); len(got) != 1 {
		t.Fatalf("attempts = %v", got)
	}
}

func TestRunRetriesDoNotCountAsSteps(t *testing.T) {
	deps, _ := retryDeps(t)
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxSteps: 1}
	r := runHarness(t, context.Background(), cfg, deps, rateLimited(), textTurn("ok"))
	wantStop(t, r.state, stopCompleted, "")
}

func TestRunRetryStopsWhenBudgetRunsOutDuringBackoff(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	deps := defaultDeps(t)
	deps.Now = clock.Now
	deps.Sleep = func(context.Context, time.Duration) error { t.Fatal("must not sleep"); return nil }
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxWallClock: durationpb.New(time.Minute)}
	slowFail := &advancingModel{Fake: model.NewFake(rateLimited(), textTurn("unused")), clock: clock}
	store := newLog(t)
	deps.Log, deps.Model = store, slowFail
	state, err := Run(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	assertFolds(t, store, "r1", state)
	wantBudgetStop(t, state, budgetWall, "max_wall_clock 1m0s reached")
}

// advancingModel moves the test clock by a minute on every call.
type advancingModel struct {
	*model.Fake
	clock *stepClock
}

func (m *advancingModel) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	m.clock.t = m.clock.t.Add(time.Minute)
	return m.Fake.Generate(ctx, req)
}

func TestRunRetryParentSeq(t *testing.T) {
	deps, _ := retryDeps(t)
	r := runHarness(t, context.Background(), baseConfig(), deps, rateLimited(), rateLimited(), textTurn("ok"))
	var lastStart uint64
	var completed *adlcv1.Event
	for _, ev := range r.events {
		if ev.GetModelCallStarted() != nil {
			lastStart = ev.GetSeq()
		}
		if ev.GetModelCallCompleted() != nil {
			completed = ev
		}
	}
	if completed.GetParentSeq() != lastStart {
		t.Fatalf("parent_seq = %d, want %d", completed.GetParentSeq(), lastStart)
	}
}

func TestRunCancelDuringModelCallRecordsFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := newLog(t)
	deps, rec := retryDeps(t)
	deps.Log = store
	deps.Model = cancelingFailModel{Fake: model.NewFake(), cancel: cancel}
	state, err := Run(ctx, baseConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	events := assertFolds(t, store, "r1", state)
	wantKinds(t, events, "run_started", "model_call_started", "model_call_failed", "run_completed")
	wantStop(t, state, adlcv1.StopReason_STOP_REASON_CANCELLED, "")
	if got := willRetry(events); !reflect.DeepEqual(got, []bool{false}) {
		t.Fatalf("will_retry = %v", got)
	}
	wantWaits(t, rec)
}

// cancelingFailModel cancels the run and fails with a retryable error.
type cancelingFailModel struct {
	*model.Fake
	cancel context.CancelFunc
}

func (m cancelingFailModel) Generate(context.Context, *model.Request) (*model.Response, error) {
	m.cancel()
	return nil, rateLimited().Err
}
