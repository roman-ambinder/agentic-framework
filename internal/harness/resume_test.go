package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

// twoReads is a two-turn script; its log has these events:
//
//	1 run_started  2 model_call_started  3 model_call_completed (t1, t2)
//	4 tool_call_requested t1  5 policy_decided  6 tool_call_completed
//	7 tool_call_requested t2  8 policy_decided  9 tool_call_completed
//	10 checkpoint  11 model_call_started  12 model_call_completed  13 run_completed
func twoReads() []model.FakeTurn {
	return []model.FakeTurn{toolTurn(readCall("t1", "a"), readCall("t2", "b")), textTurn("done")}
}

// crashRun runs cfg on a fresh store until the log has n events, as if the
// process died right after the n-th append.
func crashRun(t *testing.T, n int, cfg Config, deps Deps, turns ...model.FakeTurn) *eventlog.Store {
	t.Helper()
	return crashRunIn(t, newLog(t), n, cfg, deps, turns...)
}

func crashRunIn(t *testing.T, store *eventlog.Store, n int, cfg Config, deps Deps, turns ...model.FakeTurn) *eventlog.Store {
	t.Helper()
	deps.Log, deps.Model = &failingLog{Log: store, n: n}, model.NewFake(turns...)
	if _, err := Run(context.Background(), cfg, deps); !errors.Is(err, errLogDown) {
		t.Fatalf("Run err = %v, want the log failure", err)
	}
	events, err := store.Read(context.Background(), cfg.RunID, 0)
	if err != nil || len(events) != n {
		t.Fatalf("crashed log has %d events (err %v), want %d", len(events), err, n)
	}
	return store
}

// resumed is the result of resumeOK.
type resumed struct {
	state *eventlog.RunState
	// events is the whole log; tail is what Resume appended.
	events, tail []*adlcv1.Event
	model        *model.Fake
}

// resumeOK resumes the run with a new fake model and checks what every
// resume must satisfy: the log was only appended to, it folds to the returned
// state, and it holds exactly one run_resumed.
func resumeOK(t *testing.T, store *eventlog.Store, cfg Config, deps Deps, turns ...model.FakeTurn) resumed {
	t.Helper()
	ctx := context.Background()
	before, err := store.Read(ctx, cfg.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	fake := model.NewFake(turns...)
	deps.Log, deps.Model = store, fake
	state, err := Resume(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	events := assertFolds(t, store, cfg.RunID, state)
	for i, ev := range before {
		if !proto.Equal(ev, events[i]) {
			t.Fatalf("event %d changed by Resume", i+1)
		}
	}
	if n := len(pick(events, (*adlcv1.Event).GetRunResumed)); n != 1 {
		t.Fatalf("log has %d run_resumed events, want 1", n)
	}
	return resumed{state: state, events: events, tail: events[len(before):], model: fake}
}

func resumeDeps(t *testing.T) Deps { return defaultDeps(t) }

// firstRequestResults returns the tool results in the last message of the
// model's first request.
func firstRequestResults(t *testing.T, f *model.Fake) []model.ToolResult {
	t.Helper()
	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("the model was not called")
	}
	msgs := reqs[0].Messages
	var out []model.ToolResult
	for _, p := range msgs[len(msgs)-1].Parts {
		if p.ToolResult != nil {
			out = append(out, *p.ToolResult)
		}
	}
	return out
}

func TestResumeUnknownRun(t *testing.T) {
	deps := resumeDeps(t)
	deps.Log, deps.Model = newLog(t), model.NewFake()
	_, err := Resume(context.Background(), baseConfig(), deps)
	if !errors.Is(err, eventlog.ErrRunNotFound) {
		t.Fatalf("err = %v, want ErrRunNotFound", err)
	}
}

func TestResumeIgnoresTask(t *testing.T) {
	store := crashRun(t, 2, baseConfig(), resumeDeps(t), twoReads()...)
	cfg := baseConfig()
	cfg.Task = ""
	r := resumeOK(t, store, cfg, resumeDeps(t), twoReads()...)
	wantStop(t, r.state, stopCompleted, "")
}

func TestResumeFinishedRun(t *testing.T) {
	store := newLog(t)
	deps := resumeDeps(t)
	deps.Log, deps.Model = store, model.NewFake(textTurn("done"))
	if _, err := Run(context.Background(), baseConfig(), deps); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Read(context.Background(), "r1", 0)
	deps.Model = model.NewFake()
	if _, err := Resume(context.Background(), baseConfig(), deps); !errors.Is(err, ErrRunFinished) {
		t.Fatalf("err = %v, want ErrRunFinished", err)
	}
	if after, _ := store.Read(context.Background(), "r1", 0); len(after) != len(before) {
		t.Fatalf("%d events appended to a finished run", len(after)-len(before))
	}
}

func TestResumePausedRun(t *testing.T) {
	store := newLog(t)
	deps := Deps{Tools: staticTools(t), Policy: needsApproval(t), Log: store}
	deps.Model = model.NewFake(toolTurn(readCall("t1", "a")))
	st, err := Run(context.Background(), baseConfig(), deps)
	if err != nil || st.Status != eventlog.StatusPaused {
		t.Fatalf("setup: %v %+v", err, st)
	}
	if _, err := Resume(context.Background(), baseConfig(), deps); !errors.Is(err, ErrRunPaused) {
		t.Fatalf("err = %v, want ErrRunPaused", err)
	}
}

func TestResumeAgentMismatch(t *testing.T) {
	store := crashRun(t, 2, baseConfig(), resumeDeps(t), twoReads()...)
	cfg := baseConfig()
	cfg.AgentVersion = "test@2"
	deps := resumeDeps(t)
	deps.Log, deps.Model = store, model.NewFake()
	_, err := Resume(context.Background(), cfg, deps)
	if !errors.Is(err, ErrAgentMismatch) || !strings.Contains(err.Error(), `run has "test@1", config has "test@2"`) {
		t.Fatalf("err = %v, want ErrAgentMismatch", err)
	}
}

func TestResumeCheckpointMismatch(t *testing.T) {
	store := crashRun(t, 6, baseConfig(), resumeDeps(t), twoReads()...)
	_, err := store.Append(context.Background(), &adlcv1.Event{RunId: "r1", Payload: &adlcv1.Event_Checkpoint{
		Checkpoint: &adlcv1.Checkpoint{Step: 1, StateDigest: "sha256:bad"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	deps := resumeDeps(t)
	deps.Log, deps.Model = store, model.NewFake()
	_, err = Resume(context.Background(), baseConfig(), deps)
	if !errors.Is(err, ErrCheckpointMismatch) || !strings.Contains(err.Error(), "seq 7") {
		t.Fatalf("err = %v, want ErrCheckpointMismatch at seq 7", err)
	}
}

func TestResumeVerifiesRealCheckpoint(t *testing.T) {
	store := crashRun(t, 11, baseConfig(), resumeDeps(t), twoReads()...) // 10 is a good checkpoint
	r := resumeOK(t, store, baseConfig(), resumeDeps(t), twoReads()[1:]...)
	if rr := pick(r.events, (*adlcv1.Event).GetRunResumed)[0]; rr.GetVerifiedCheckpointSeq() != 10 || rr.GetNextStep() != 2 {
		t.Fatalf("run_resumed = %v", rr)
	}
}

func TestResumeDuringModelCall(t *testing.T) { // case C
	store := crashRun(t, 2, baseConfig(), resumeDeps(t), twoReads()...)
	r := resumeOK(t, store, baseConfig(), resumeDeps(t), twoReads()...)
	wantKinds(t, r.tail, "run_resumed", "model_call_started", "model_call_completed",
		"tool_call_requested", "policy_decided", "tool_call_completed",
		"tool_call_requested", "policy_decided", "tool_call_completed", "checkpoint",
		"model_call_started", "model_call_completed", "run_completed")
	wantStop(t, r.state, stopCompleted, "")
	if rr := r.tail[0].GetRunResumed(); rr.GetNextStep() != 1 || rr.GetAbandonedToolCalls() != 0 || rr.GetVerifiedCheckpointSeq() != 0 {
		t.Fatalf("run_resumed = %v", rr)
	}
}

func TestResumeAfterFailedModelCall(t *testing.T) { // case C, second form
	deps := resumeDeps(t)
	deps.Sleep = func(context.Context, time.Duration) error { return nil }
	store := crashRun(t, 3, baseConfig(), deps, rateLimited(), textTurn("done"))
	wantKindsOf(t, store, "run_started", "model_call_started", "model_call_failed")
	r := resumeOK(t, store, baseConfig(), deps, textTurn("done"))
	wantStop(t, r.state, stopCompleted, "")
}

func wantKindsOf(t *testing.T, store *eventlog.Store, want ...string) {
	t.Helper()
	evs, err := store.Read(context.Background(), "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds(t, evs, want...)
}

func TestResumeDuringToolCall(t *testing.T) { // case A
	store := crashRun(t, 5, baseConfig(), resumeDeps(t), twoReads()...)
	r := resumeOK(t, store, baseConfig(), resumeDeps(t), twoReads()[1:]...)
	wantKinds(t, r.tail[:5], "run_resumed", "tool_call_completed", "tool_call_requested", "tool_call_completed", "model_call_started")
	ab := r.tail[1]
	if c := ab.GetToolCallCompleted(); c.GetCallId() != "t1" || !c.GetIsError() || string(c.GetOutput()) != AbandonedOutput {
		t.Fatalf("abandoned result = %v", c)
	}
	if ab.GetParentSeq() != 4 {
		t.Fatalf("parent_seq = %d, want 4 (the tool_call_requested of t1)", ab.GetParentSeq())
	}
	if rr := r.tail[0].GetRunResumed(); rr.GetAbandonedToolCalls() != 2 || rr.GetNextStep() != 2 {
		t.Fatalf("run_resumed = %v", rr)
	}
	got := firstRequestResults(t, r.model)
	if len(got) != 2 || got[0].CallID != "t1" || got[0].Content != AbandonedOutput || !got[0].IsError ||
		got[1].CallID != "t2" || got[1].Content != NotRunOutput {
		t.Fatalf("the model saw %+v", got)
	}
	wantStop(t, r.state, stopCompleted, "")
}

func TestResumeBeforeToolCallsRequested(t *testing.T) { // case B
	store := crashRun(t, 3, baseConfig(), resumeDeps(t), twoReads()...)
	r := resumeOK(t, store, baseConfig(), resumeDeps(t), twoReads()[1:]...)
	wantKinds(t, r.tail[:6], "run_resumed", "tool_call_requested", "tool_call_completed",
		"tool_call_requested", "tool_call_completed", "model_call_started")
	reqs := pick(r.tail, (*adlcv1.Event).GetToolCallRequested)
	if reqs[0].GetCall().GetId() != "t1" || reqs[1].GetCall().GetId() != "t2" ||
		reqs[0].GetRiskClass() != adlcv1.RiskClass_RISK_CLASS_READ || reqs[0].GetServer() != "fs" {
		t.Fatalf("requested = %v", reqs)
	}
	for i, c := range pick(r.tail, (*adlcv1.Event).GetToolCallCompleted) {
		if !c.GetIsError() || string(c.GetOutput()) != NotRunOutput || c.GetCallId() != fmt.Sprintf("t%d", i+1) {
			t.Fatalf("result %d = %v", i, c)
		}
	}
	if rr := r.tail[0].GetRunResumed(); rr.GetAbandonedToolCalls() != 2 {
		t.Fatalf("run_resumed = %v", rr)
	}
	for i, ev := range r.tail[1:5] { // each result's parent is its own request
		if i%2 == 1 && ev.GetParentSeq() != r.tail[i].GetSeq() {
			t.Fatalf("result parent_seq = %d, want %d", ev.GetParentSeq(), r.tail[i].GetSeq())
		}
	}
}

func TestResumeBeforeToolCallsRequestedUnknownTool(t *testing.T) { // case B, tool not in the router
	turns := []model.FakeTurn{toolTurn(model.ToolCall{ID: "t1", Name: "gone__tool"}), textTurn("done")}
	store := crashRun(t, 3, baseConfig(), resumeDeps(t), turns...)
	r := resumeOK(t, store, baseConfig(), resumeDeps(t), turns[1:]...)
	req := pick(r.tail, (*adlcv1.Event).GetToolCallRequested)[0]
	if req.GetRiskClass() != adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED || req.GetServer() != "" {
		t.Fatalf("requested = %v", req)
	}
}

func TestResumeAfterLastResult(t *testing.T) { // case D
	store := crashRun(t, 9, baseConfig(), resumeDeps(t), twoReads()...)
	r := resumeOK(t, store, baseConfig(), resumeDeps(t), twoReads()[1:]...)
	wantKinds(t, r.tail, "run_resumed", "model_call_started", "model_call_completed", "run_completed")
	if rr := r.tail[0].GetRunResumed(); rr.GetAbandonedToolCalls() != 0 {
		t.Fatalf("run_resumed = %v", rr)
	}
}

func TestResumeAfterFinalAnswer(t *testing.T) { // case F
	tests := []struct {
		name   string
		turn   model.FakeTurn
		stop   adlcv1.StopReason
		result string
		detail string
	}{
		{"end turn", textTurn("the answer"), stopCompleted, "the answer", ""},
		{"max tokens", finishTurn("cut", adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS), stopError, "", "output limit reached"},
		{"refusal", finishTurn("no", adlcv1.FinishReason_FINISH_REASON_REFUSAL), stopError, "", "model refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := crashRun(t, 3, baseConfig(), resumeDeps(t), tt.turn)
			r := resumeOK(t, store, baseConfig(), resumeDeps(t))
			wantKinds(t, r.tail, "run_resumed", "run_completed")
			wantStop(t, r.state, tt.stop, tt.detail)
			if r.state.Result != tt.result {
				t.Fatalf("result = %q, want %q", r.state.Result, tt.result)
			}
			if n := len(r.model.Requests()); n != 0 {
				t.Fatalf("the model was called %d times", n)
			}
		})
	}
}

func finishTurn(text string, f adlcv1.FinishReason) model.FakeTurn {
	turn := textTurn(text)
	turn.Response.Finish = f
	return turn
}

func TestResumeAfterDeny(t *testing.T) { // case G
	pol, err := policy.NewStatic([]policy.Rule{{ID: "no-read", Risk: adlcv1.RiskClass_RISK_CLASS_READ, Decision: adlcv1.Decision_DECISION_DENY}},
		adlcv1.Decision_DECISION_ALLOW)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Tools: staticTools(t), Policy: pol}
	turns := []model.FakeTurn{toolTurn(readCall("t1", "a")), textTurn("unused")}
	store := crashRun(t, 5, baseConfig(), deps, turns...)
	r := resumeOK(t, store, baseConfig(), deps)
	wantKinds(t, r.tail, "run_resumed", "run_completed")
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_DENIED, "rule no-read: matched rule no-read")
	if n := len(r.model.Requests()); n != 0 {
		t.Fatalf("the model was called %d times", n)
	}
}

func needsApproval(t *testing.T) policy.Client {
	t.Helper()
	pol, err := policy.NewStatic(nil, adlcv1.Decision_DECISION_NEEDS_APPROVAL)
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

func TestResumeBeforeRunCompletedOnApproval(t *testing.T) { // case E
	deps := Deps{Tools: staticTools(t), Policy: needsApproval(t)}
	store := crashRun(t, 6, baseConfig(), deps, toolTurn(readCall("t1", "a")))
	r := resumeOK(t, store, baseConfig(), deps)
	wantKinds(t, r.tail, "run_resumed", "run_completed")
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL, "")
	if r.state.Status != eventlog.StatusPaused {
		t.Fatalf("status = %v, want paused", r.state.Status)
	}
	if n := len(r.model.Requests()); n != 0 {
		t.Fatalf("the model was called %d times", n)
	}
}

func TestResumeKeepsBudget(t *testing.T) {
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxSteps: 3}
	turns := toolTurns(5, model.Usage{})
	// Two steps of 6 events each follow run_started; stop right after the second checkpoint.
	store := crashRun(t, 13, cfg, resumeDeps(t), turns...)
	r := resumeOK(t, store, cfg, resumeDeps(t), turns[2:]...)
	if n := countModelCalls(r.events); n != 3 {
		t.Fatalf("%d model calls in total, want 3", n)
	}
	wantBudgetStop(t, r.state, budgetSteps, "max_steps 3 reached")
}

func TestResumeKeepsToolCallsAndTokens(t *testing.T) {
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxToolCalls: 2}
	turns := toolTurns(4, model.Usage{})
	store := crashRun(t, 13, cfg, resumeDeps(t), turns...)
	r := resumeOK(t, store, cfg, resumeDeps(t), turns[2:]...)
	wantBudgetStop(t, r.state, budgetToolCalls, "max_tool_calls 2 reached")

	cfg.Budget = &adlcv1.Budget{MaxTokens: 20}
	usage := model.Usage{InputTokens: 6, OutputTokens: 4}
	turns = toolTurns(5, usage)
	store = crashRunIn(t, newLog(t), 13, cfg, resumeDeps(t), turns...)
	r = resumeOK(t, store, cfg, resumeDeps(t), turns[2:]...)
	wantBudgetStop(t, r.state, budgetTokens, "max_tokens 20 reached (used 20)")
}

// clockedLog opens a store whose event times come from clock.
func clockedLog(t *testing.T, clock *stepClock) *eventlog.Store {
	t.Helper()
	s, err := eventlog.Open(context.Background(), filepath.Join(t.TempDir(), "events.db"), eventlog.WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestResumeWallClockUsesTimeSpent(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	advance := func(name string, d time.Duration) toolrouter.StaticTool {
		return funcTool(name, adlcv1.RiskClass_RISK_CLASS_READ, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
			clock.t = clock.t.Add(d)
			return toolrouter.Result{Output: []byte("ok")}, nil
		})
	}
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxWallClock: durationpb.New(60 * time.Second)}
	deps := Deps{
		Tools:  staticTools(t, advance("x__slow50", 50*time.Second), advance("x__slow11", 11*time.Second)),
		Policy: policy.AllowAll{}, Now: clock.Now,
	}
	// The first run uses 50 s and dies after its checkpoint (event 7).
	store := crashRunIn(t, clockedLog(t, clock), 7, cfg, deps, toolTurn(model.ToolCall{ID: "t1", Name: "x__slow50"}))
	clock.t = clock.t.Add(24 * time.Hour) // the downtime must not count
	r := resumeOK(t, store, cfg, deps,
		toolTurn(model.ToolCall{ID: "t2", Name: "x__slow11"}), textTurn("unused"))
	wantBudgetStop(t, r.state, budgetWall, "max_wall_clock 1m0s reached")
	if n := countModelCalls(r.events); n != 2 {
		t.Fatalf("%d model calls, want 2", n)
	}
}

func TestResumeWallClockAlreadyUsedUp(t *testing.T) {
	clock := &stepClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	slow := funcTool("x__slow", adlcv1.RiskClass_RISK_CLASS_READ, func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		clock.t = clock.t.Add(2 * time.Minute)
		return toolrouter.Result{Output: []byte("ok")}, nil
	})
	cfg := baseConfig()
	cfg.Budget = &adlcv1.Budget{MaxWallClock: durationpb.New(time.Minute)}
	deps := Deps{Tools: staticTools(t, slow), Policy: policy.AllowAll{}, Now: clock.Now}
	store := crashRunIn(t, clockedLog(t, clock), 7, cfg, deps, toolTurn(model.ToolCall{ID: "t1", Name: "x__slow"}))
	r := resumeOK(t, store, cfg, deps)
	wantKinds(t, r.tail, "run_resumed", "run_completed")
	wantBudgetStop(t, r.state, budgetWall, "max_wall_clock 1m0s reached")
}

// readThenCancel cancels the caller's context once the run has been read.
type readThenCancel struct {
	Log
	cancel context.CancelFunc
}

func (r readThenCancel) Read(ctx context.Context, runID string, after uint64) ([]*adlcv1.Event, error) {
	evs, err := r.Log.Read(ctx, runID, after)
	r.cancel()
	return evs, err
}

func TestResumeCancelled(t *testing.T) {
	store := crashRun(t, 2, baseConfig(), resumeDeps(t), twoReads()...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := resumeDeps(t)
	deps.Log, deps.Model = readThenCancel{Log: store, cancel: cancel}, model.NewFake()
	st, err := Resume(ctx, baseConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	wantStop(t, st, adlcv1.StopReason_STOP_REASON_CANCELLED, "")
	assertFolds(t, store, "r1", st)
}

func TestResumeTwice(t *testing.T) {
	// A run that dies again after being resumed can be resumed again.
	store := newLog(t)
	cfg := baseConfig()
	crashRunIn(t, store, 2, cfg, resumeDeps(t), twoReads()...)
	deps := resumeDeps(t)
	deps.Log, deps.Model = &failingLog{Log: store, n: 3}, model.NewFake(twoReads()...)
	if _, err := Resume(context.Background(), cfg, deps); !errors.Is(err, errLogDown) {
		t.Fatalf("err = %v", err)
	}
	evs, _ := store.Read(context.Background(), "r1", 0)
	wantKinds(t, evs, "run_started", "model_call_started", "run_resumed", "model_call_started", "model_call_completed")
	deps.Log, deps.Model = store, model.NewFake(twoReads()[1:]...)
	st, err := Resume(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	wantStop(t, st, stopCompleted, "")
	events := assertFolds(t, store, "r1", st)
	if n := len(pick(events, (*adlcv1.Event).GetRunResumed)); n != 2 {
		t.Fatalf("%d run_resumed events, want 2", n)
	}
	if got := firstRequestResults(t, deps.Model.(*model.Fake)); len(got) != 2 {
		t.Fatalf("model saw %+v", got)
	}
}
