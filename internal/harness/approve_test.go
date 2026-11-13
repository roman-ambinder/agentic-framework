package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// countingPolicy counts Authorize calls.
type countingPolicy struct {
	inner policy.Client
	mu    sync.Mutex
	n     int
}

func (p *countingPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	p.mu.Lock()
	p.n++
	p.mu.Unlock()
	return p.inner.Authorize(ctx, req)
}

func (p *countingPolicy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

// writeTool is "x__write" (WRITE_LOCAL); it counts its calls.
func writeTool(calls *int) toolrouter.StaticTool {
	return funcTool("x__write", adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL, func(_ context.Context, args json.RawMessage) (toolrouter.Result, error) {
		*calls++
		return toolrouter.Result{Output: []byte("wrote " + string(args))}, nil
	})
}

func writeCall(id string) model.ToolCall {
	return model.ToolCall{ID: id, Name: "x__write", Arguments: json.RawMessage(`{"path":"p"}`)}
}

func writesNeedApproval(t *testing.T) *countingPolicy {
	t.Helper()
	pol, err := policy.NewStatic([]policy.Rule{{
		ID: "writes", Risk: adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL, Decision: adlcv1.Decision_DECISION_NEEDS_APPROVAL,
	}}, adlcv1.Decision_DECISION_ALLOW)
	if err != nil {
		t.Fatal(err)
	}
	return &countingPolicy{inner: pol}
}

// approveEnv is a paused run plus what Approve needs.
type approveEnv struct {
	store *eventlog.Store
	deps  Deps
	cfg   Config
	pol   *countingPolicy
	calls *int
}

// pausedRun runs the agent with the given turns and asserts it paused.
func pausedRun(t *testing.T, cfg Config, extra []toolrouter.StaticTool, hooks []Hook, store *eventlog.Store, now func() time.Time, turns ...model.FakeTurn) *approveEnv {
	t.Helper()
	calls := new(int)
	pol := writesNeedApproval(t)
	cfg.Hooks = hooks
	if store == nil {
		store = newLog(t)
	}
	deps := Deps{
		Tools:  staticTools(t, append([]toolrouter.StaticTool{writeTool(calls)}, extra...)...),
		Policy: pol, Log: store, Model: model.NewFake(turns...), Now: now,
	}
	st, err := Run(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != eventlog.StatusPaused {
		t.Fatalf("status = %v, want paused", st.Status)
	}
	return &approveEnv{store: store, deps: deps, cfg: cfg, pol: pol, calls: calls}
}

func newPaused(t *testing.T, turns ...model.FakeTurn) *approveEnv {
	t.Helper()
	return pausedRun(t, baseConfig(), nil, nil, nil, nil, turns...)
}

type approved struct {
	state        *eventlog.RunState
	events, tail []*adlcv1.Event
	model        *model.Fake
}

// approve calls Approve with a new fake model and checks the invariants: the
// log was only appended to and folds to the returned state.
func (e *approveEnv) approve(t *testing.T, res Resolution, turns ...model.FakeTurn) approved {
	t.Helper()
	before, err := e.store.Read(context.Background(), e.cfg.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	fake := model.NewFake(turns...)
	deps := e.deps
	deps.Model = fake
	state, err := Approve(context.Background(), e.cfg, deps, res)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	events := assertFolds(t, e.store, e.cfg.RunID, state)
	for i, ev := range before {
		if !proto.Equal(ev, events[i]) {
			t.Fatalf("event %d changed by Approve", i+1)
		}
	}
	return approved{state: state, events: events, tail: events[len(before):], model: fake}
}

func (e *approveEnv) approveErr(t *testing.T, cfg Config, res Resolution) error {
	t.Helper()
	before, _ := e.store.Read(context.Background(), cfg.RunID, 0)
	deps := e.deps
	deps.Model = model.NewFake()
	st, err := Approve(context.Background(), cfg, deps, res)
	if err == nil || st != nil {
		t.Fatalf("Approve = %v, %v; want an error", st, err)
	}
	after, _ := e.store.Read(context.Background(), cfg.RunID, 0)
	if len(after) != len(before) {
		t.Fatalf("a refused Approve appended %d events", len(after)-len(before))
	}
	return err
}

func yes(id string) Resolution { return Resolution{ApprovalID: id, Approved: true, By: "bob"} }

func TestApproveRunsWaitingCall(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	reqSeq := requestSeq(mustRead(t, e), "a-w1")
	r := e.approve(t, yes("a-w1"), textTurn("done"))
	if *e.calls != 1 {
		t.Fatalf("tool called %d times", *e.calls)
	}
	wantKinds(t, r.tail[:4], "approval_resolved", "tool_call_completed", "checkpoint", "model_call_started")
	ar := r.tail[0]
	if ar.GetApprovalResolved().GetResolvedBy() != "bob" || ar.GetParentSeq() != reqSeq {
		t.Fatalf("approval_resolved = %v (want parent %d)", ar, reqSeq)
	}
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_COMPLETED, "")
	if r.state.Result != "done" {
		t.Fatalf("result = %q", r.state.Result)
	}
	res := firstRequestResults(t, r.model)
	if len(res) != 1 || res[0].CallID != "w1" || res[0].Content != `wrote {"path":"p"}` {
		t.Fatalf("first request results = %+v", res)
	}
}

func mustRead(t *testing.T, e *approveEnv) []*adlcv1.Event {
	t.Helper()
	evs, err := e.store.Read(context.Background(), e.cfg.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestApproveDoesNotAskPolicyAgain(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	if e.pol.count() != 1 {
		t.Fatalf("policy asked %d times by Run", e.pol.count())
	}
	r := e.approve(t, yes("a-w1"), textTurn("done"))
	if e.pol.count() != 1 {
		t.Fatalf("policy asked %d times after Approve", e.pol.count())
	}
	if n := len(pick(r.events, (*adlcv1.Event).GetPolicyDecided)); n != 1 {
		t.Fatalf("%d policy_decided events", n)
	}
}

func TestApproveRunsPreToolHooks(t *testing.T) {
	h := &testHook{name: "spy"}
	e := pausedRun(t, baseConfig(), nil, []Hook{h}, nil, nil, toolTurn(writeCall("w1")))
	e.approve(t, yes("a-w1"), textTurn("done"))
	if len(h.pre) != 2 || h.pre[0] != "x__write" || h.pre[1] != "x__write" {
		t.Fatalf("pre = %v", h.pre)
	}
	if len(h.post) != 1 || h.post[0] != "x__write" {
		t.Fatalf("post = %v", h.post)
	}
}

// denySecond denies its second PreTool call.
type denySecond struct{ n int }

func (h *denySecond) Name() string { return "second" }
func (h *denySecond) PreTool(context.Context, model.ToolCall, toolrouter.Tool) (HookDecision, error) {
	h.n++
	return HookDecision{Deny: h.n >= 2, Reason: "no"}, nil
}

func (h *denySecond) PostTool(context.Context, model.ToolCall, toolrouter.Result) error { return nil }

func TestApproveHookDenies(t *testing.T) {
	e := pausedRun(t, baseConfig(), nil, []Hook{&denySecond{}}, nil, nil, toolTurn(writeCall("w1")))
	r := e.approve(t, yes("a-w1"))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_DENIED, "rule hook:second: no")
	if *e.calls != 0 {
		t.Fatalf("tool called %d times", *e.calls)
	}
}

func TestApproveRejectedCallNeverReachesTool(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	r := e.approve(t, Resolution{ApprovalID: "a-w1", By: "bob", Comment: "not now"}, textTurn("ok"))
	if *e.calls != 0 {
		t.Fatalf("tool called %d times", *e.calls)
	}
	done := pick(r.tail, (*adlcv1.Event).GetToolCallCompleted)
	if len(done) != 1 || !done[0].GetIsError() || string(done[0].GetOutput()) != "rejected by bob: not now" {
		t.Fatalf("tool_call_completed = %v", done)
	}
	res := firstRequestResults(t, r.model)
	if len(res) != 1 || !res[0].IsError || res[0].Content != "rejected by bob: not now" {
		t.Fatalf("first request results = %+v", res)
	}
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_COMPLETED, "")
	if r.state.Denials != 1 {
		t.Fatalf("denials = %d", r.state.Denials)
	}
}

func TestRejectedOutput(t *testing.T) {
	tests := []struct{ by, comment, want string }{
		{"bob", "", "rejected by bob"},
		{"bob", "no", "rejected by bob: no"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := RejectedOutput(tt.by, tt.comment); got != tt.want {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestApproveRunsRestOfStep(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1"), readCall("r1", "a")))
	r := e.approve(t, yes("a-w1"), textTurn("done"))
	wantKinds(t, r.tail[:7], "approval_resolved", "tool_call_completed",
		"tool_call_requested", "policy_decided", "tool_call_completed", "checkpoint", "model_call_started")
	if pd := r.tail[3].GetPolicyDecided(); pd.GetCallId() != "r1" || pd.GetDecision() != adlcv1.Decision_DECISION_ALLOW {
		t.Fatalf("policy_decided = %v", pd)
	}
}

func TestApprovePausesAgain(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1"), writeCall("w2")))
	r := e.approve(t, yes("a-w1"))
	if r.state.Status != eventlog.StatusPaused || len(r.state.PendingApprovals) != 1 || r.state.PendingApprovals[0] != "a-w2" {
		t.Fatalf("state = %+v", r.state)
	}
	if n := len(r.model.Requests()); n != 0 {
		t.Fatalf("model called %d times", n)
	}
	r = e.approve(t, yes("a-w2"), textTurn("done"))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_COMPLETED, "")
	if *e.calls != 2 {
		t.Fatalf("tool called %d times", *e.calls)
	}
}

// severalPending hand-builds a run paused on a-w1 and a-w2.
func severalPending(t *testing.T) *approveEnv {
	t.Helper()
	store := newLog(t)
	ctx := context.Background()
	call := func(id string) *adlcv1.ToolCall {
		return &adlcv1.ToolCall{Id: id, Name: "x__write", ArgumentsJson: `{"path":"p"}`}
	}
	evs := []*adlcv1.Event{
		{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{AgentVersion: "test@1", Task: "do it"}}},
		{Payload: &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{CallId: "m1", Step: 1}}},
		{Payload: &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: &adlcv1.ModelCallCompleted{
			CallId: "m1", ToolCalls: []*adlcv1.ToolCall{call("w1"), call("w2")},
		}}},
	}
	for _, id := range []string{"w1", "w2"} {
		evs = append(evs,
			&adlcv1.Event{Payload: &adlcv1.Event_ToolCallRequested{ToolCallRequested: &adlcv1.ToolCallRequested{
				Call: call(id), RiskClass: adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL, Server: "x",
			}}},
			&adlcv1.Event{Payload: &adlcv1.Event_PolicyDecided{PolicyDecided: &adlcv1.PolicyDecided{
				CallId: id, Decision: adlcv1.Decision_DECISION_NEEDS_APPROVAL,
			}}},
			&adlcv1.Event{Payload: &adlcv1.Event_ApprovalRequested{ApprovalRequested: &adlcv1.ApprovalRequested{
				ApprovalId: "a-" + id, CallId: id, Summary: "x__write",
			}}},
		)
	}
	evs = append(evs, &adlcv1.Event{Payload: &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{
		StopReason: adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL,
	}}})
	for _, ev := range evs {
		ev.RunId, ev.AgentId = "r1", "test@1"
		if _, err := store.Append(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	calls := new(int)
	pol := writesNeedApproval(t)
	return &approveEnv{store: store, cfg: baseConfig(), pol: pol, calls: calls, deps: Deps{
		Tools: staticTools(t, writeTool(calls)), Policy: pol, Log: store,
	}}
}

func TestApproveSeveralPending(t *testing.T) {
	e := severalPending(t)
	r := e.approve(t, yes("a-w1"))
	wantKinds(t, r.tail, "approval_resolved", "run_completed")
	if r.state.Status != eventlog.StatusPaused || len(r.model.Requests()) != 0 || *e.calls != 0 {
		t.Fatalf("state %+v, model calls %d, tool calls %d", r.state, len(r.model.Requests()), *e.calls)
	}
	r = e.approve(t, Resolution{ApprovalID: "a-w2", By: "ann", Comment: "x"}, textTurn("done"))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_COMPLETED, "")
	if *e.calls != 1 {
		t.Fatalf("tool called %d times, want 1 (w1 only)", *e.calls)
	}
	done := pick(r.tail, (*adlcv1.Event).GetToolCallCompleted)
	if len(done) != 2 || done[0].GetCallId() != "w1" || done[0].GetIsError() ||
		done[1].GetCallId() != "w2" || string(done[1].GetOutput()) != "rejected by ann: x" {
		t.Fatalf("results = %v", done)
	}
}

func TestResolveApprovalRefusesLast(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	n := len(mustRead(t, e))
	_, err := ResolveApproval(context.Background(), e.store, "r1", yes("a-w1"))
	if !errors.Is(err, ErrContinueRun) || len(mustRead(t, e)) != n {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveUnknownRun(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	cfg := baseConfig()
	cfg.RunID = "nope"
	if err := e.approveErr(t, cfg, yes("a-w1")); !errors.Is(err, eventlog.ErrRunNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveUnknownApproval(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	if err := e.approveErr(t, e.cfg, yes("a-nope")); !errors.Is(err, ErrUnknownApproval) {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveAlreadyResolved(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	e.approve(t, yes("a-w1"), textTurn("done"))
	if err := e.approveErr(t, e.cfg, yes("a-w1")); !errors.Is(err, ErrApprovalResolved) {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveRunNotPaused(t *testing.T) {
	store := newLog(t)
	deps := Deps{Tools: staticTools(t, writeTool(new(int))), Policy: writesNeedApproval(t)}
	// events: run_started, model_call_started, model_call_completed,
	// tool_call_requested, policy_decided, approval_requested
	crashRunIn(t, store, 6, baseConfig(), deps, toolTurn(writeCall("w1")))
	deps.Log = store
	e := &approveEnv{store: store, cfg: baseConfig(), deps: deps}
	err := e.approveErr(t, e.cfg, yes("a-w1"))
	if !errors.Is(err, ErrRunNotPaused) || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveAgentMismatch(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	cfg := e.cfg
	cfg.AgentVersion = "other@2"
	if err := e.approveErr(t, cfg, yes("a-w1")); !errors.Is(err, ErrAgentMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveNeedsBy(t *testing.T) {
	e := newPaused(t, toolTurn(writeCall("w1")))
	err := e.approveErr(t, e.cfg, Resolution{ApprovalID: "a-w1", Approved: true})
	if !strings.Contains(err.Error(), "resolved by is empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestApproveWallClockExcludesPause(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	slow := funcTool("x__slow10", adlcv1.RiskClass_RISK_CLASS_READ, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		clock.t = clock.t.Add(10 * time.Second)
		return toolrouter.Result{Output: []byte("ok")}, nil
	})
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxWallClock: durationpb.New(60 * time.Second)}
	e := pausedRun(t, cfg, []toolrouter.StaticTool{slow}, nil, clockedLog(t, clock), clock.Now,
		toolTurn(model.ToolCall{ID: "t1", Name: "x__slow10"}, writeCall("w1")))
	clock.t = clock.t.Add(time.Hour)
	r := e.approve(t, yes("a-w1"), textTurn("done"))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_COMPLETED, "")
}
