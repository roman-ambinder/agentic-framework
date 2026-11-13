package harness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// forkOrigin runs run r1 on a new store: two read calls, then "done".
// Its log has 16 events with checkpoints at seq 7 (step 1) and 13 (step 2).
func forkOrigin(t *testing.T) (*eventlog.Store, []*adlcv1.Event) {
	t.Helper()
	turns := []model.FakeTurn{toolTurn(readCall("t1", "a")), toolTurn(readCall("t2", "b")), textTurn("done")}
	store := newLog(t)
	deps := defaultDeps(t)
	deps.Log, deps.Model = store, model.NewFake(turns...)
	if _, err := Run(context.Background(), baseConfig(), deps); err != nil {
		t.Fatal(err)
	}
	return store, readAll(t, store, "r1")
}

func readAll(t *testing.T, log Log, runID string) []*adlcv1.Event {
	t.Helper()
	evs, err := log.Read(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func forkCfg() Config {
	cfg := baseConfig()
	cfg.RunID, cfg.AgentID, cfg.AgentVersion = "f1", "other@2", "other@2"
	return cfg
}

// forkOK forks r1 at step with a new fake model and returns the result.
func forkOK(t *testing.T, store *eventlog.Store, cfg Config, step uint32, turns ...model.FakeTurn) (*eventlog.RunState, *model.Fake) {
	t.Helper()
	deps := defaultDeps(t)
	fake := model.NewFake(turns...)
	deps.Log, deps.Model = store, fake
	st, err := Fork(context.Background(), cfg, deps, ForkPoint{RunID: "r1", AtStep: step})
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	return st, fake
}

func TestForkEventsReturnsPrefix(t *testing.T) {
	_, evs := forkOrigin(t)
	if len(evs) != 16 {
		t.Fatalf("origin has %d events, want 16", len(evs))
	}
	for _, tt := range []struct {
		step uint32
		n    int
	}{{1, 7}, {2, 13}} {
		got, err := ForkEvents(evs, tt.step)
		if err != nil || len(got) != tt.n {
			t.Fatalf("step %d: %d events, err %v; want %d", tt.step, len(got), err, tt.n)
		}
		if cp := got[len(got)-1].GetCheckpoint(); cp == nil || cp.GetStep() != tt.step {
			t.Fatalf("step %d: last event = %v", tt.step, got[len(got)-1])
		}
	}
}

func TestForkEventsNoCheckpoint(t *testing.T) {
	_, evs := forkOrigin(t)
	_, err := ForkEvents(evs, 3)
	if !errors.Is(err, ErrNoCheckpoint) || !strings.Contains(err.Error(), "checkpointed steps: 1, 2") {
		t.Fatalf("err = %v", err)
	}
	_, err = ForkEvents(evs[:5], 1)
	if !errors.Is(err, ErrNoCheckpoint) || !strings.Contains(err.Error(), "checkpointed steps: none") {
		t.Fatalf("err = %v", err)
	}
	if _, err := ForkEvents(nil, 1); !errors.Is(err, eventlog.ErrRunNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestForkEventsStepZero(t *testing.T) {
	_, evs := forkOrigin(t)
	_, err := ForkEvents(evs, 0)
	if err == nil || err.Error() != "harness: fork: step must be at least 1" {
		t.Fatalf("err = %v", err)
	}
}

func TestForkContinuesFromStep(t *testing.T) {
	store, origin := forkOrigin(t)
	st, fake := forkOK(t, store, forkCfg(), 1, textTurn("forked"))
	wantStop(t, st, adlcv1.StopReason_STOP_REASON_COMPLETED, "")
	if st.Result != "forked" {
		t.Fatalf("result = %q", st.Result)
	}
	prefix := origin[:7]
	want, err := Transcript(prefix, 0)
	if err != nil || len(want) != 3 {
		t.Fatalf("transcript = %d messages, err %v", len(want), err)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 || !reflect.DeepEqual(reqs[0].Messages, want) {
		t.Fatalf("first request messages = %+v\nwant %+v", reqs[0].Messages, want)
	}
	evs := assertFolds(t, store, "f1", st)
	rs := evs[0].GetRunStarted()
	if rs.GetForkedFromRunId() != "r1" || rs.GetForkedAtStep() != 1 || rs.GetTask() != "do it" || rs.GetAgentVersion() != "other@2" {
		t.Fatalf("run_started = %v", rs)
	}
	if !reflect.DeepEqual(kinds(evs[1:7]), kinds(origin[1:7])) {
		t.Fatalf("kinds %v, origin %v", kinds(evs[1:7]), kinds(origin[1:7]))
	}
	next := evs[7].GetModelCallStarted()
	if next == nil || next.GetStep() != 2 || next.GetCallId() != "m2" {
		t.Fatalf("event 8 = %v", evs[7])
	}
	if err := VerifyCheckpoints(evs); err != nil {
		t.Fatal(err)
	}
	if st.LastEventAt.Before(st.StartedAt) {
		t.Fatalf("last event %v before start %v", st.LastEventAt, st.StartedAt)
	}
	for i, ev := range evs {
		if ev.GetTime().AsTime().Before(evs[0].GetTime().AsTime()) {
			t.Fatalf("event %d is older than run_started", i+1)
		}
	}
}

func TestForkIgnoresTask(t *testing.T) {
	store, _ := forkOrigin(t)
	cfg := forkCfg()
	cfg.Task = "other"
	forkOK(t, store, cfg, 1, textTurn("x"))
	if got := readAll(t, store, "f1")[0].GetRunStarted().GetTask(); got != "do it" {
		t.Fatalf("task = %q", got)
	}
}

func TestForkLeavesOriginUnchanged(t *testing.T) {
	store, before := forkOrigin(t)
	forkOK(t, store, forkCfg(), 1, textTurn("x"))
	after := readAll(t, store, "r1")
	if len(after) != len(before) {
		t.Fatalf("origin has %d events, was %d", len(after), len(before))
	}
	for i := range before {
		if !proto.Equal(before[i], after[i]) {
			t.Fatalf("event %d changed", i+1)
		}
	}
}

func TestForkUnknownRun(t *testing.T) {
	store := newLog(t)
	deps := defaultDeps(t)
	deps.Log, deps.Model = store, model.NewFake()
	_, err := Fork(context.Background(), forkCfg(), deps, ForkPoint{RunID: "nope", AtStep: 1})
	if !errors.Is(err, eventlog.ErrRunNotFound) {
		t.Fatalf("err = %v", err)
	}
	if n := len(readAll(t, store, "f1")); n != 0 {
		t.Fatalf("%d events written", n)
	}
}

func TestForkRunIDExists(t *testing.T) {
	store, _ := forkOrigin(t)
	cfg := forkCfg()
	cfg.RunID = "r1"
	deps := defaultDeps(t)
	deps.Log, deps.Model = store, model.NewFake()
	_, err := Fork(context.Background(), cfg, deps, ForkPoint{RunID: "r1", AtStep: 1})
	if !errors.Is(err, ErrRunExists) {
		t.Fatalf("err = %v", err)
	}
	if n := len(readAll(t, store, "r1")); n != 16 {
		t.Fatalf("r1 has %d events", n)
	}
}

// flipLog returns the origin's events with one tool result flipped to an error.
type flipLog struct{ Log }

func (f flipLog) Read(ctx context.Context, runID string, from uint64) ([]*adlcv1.Event, error) {
	evs, err := f.Log.Read(ctx, runID, from)
	if err != nil || runID != "r1" {
		return evs, err
	}
	out := make([]*adlcv1.Event, len(evs))
	for i, ev := range evs {
		out[i] = proto.CloneOf(ev)
		if tc := out[i].GetToolCallCompleted(); tc != nil && out[i].GetSeq() < 7 {
			tc.IsError = true
		}
	}
	return out, nil
}

func TestForkCheckpointMismatch(t *testing.T) {
	store, _ := forkOrigin(t)
	deps := defaultDeps(t)
	deps.Log, deps.Model = flipLog{store}, model.NewFake()
	_, err := Fork(context.Background(), forkCfg(), deps, ForkPoint{RunID: "r1", AtStep: 1})
	if !errors.Is(err, ErrCheckpointMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestForkDoesNotCopyCost(t *testing.T) {
	turn := func(t model.FakeTurn) model.FakeTurn {
		t.Response.Usage = model.Usage{InputTokens: 1_000_000}
		return t
	}
	cfg := baseConfig()
	cfg.Price = model.Price{InputPerMTok: 1_000_000}
	store := newLog(t)
	deps := defaultDeps(t)
	deps.Log = store
	deps.Model = model.NewFake(turn(toolTurn(readCall("t1", "a"))), turn(toolTurn(readCall("t2", "b"))), turn(textTurn("done")))
	if _, err := Run(context.Background(), cfg, deps); err != nil {
		t.Fatal(err)
	}
	fcfg := forkCfg()
	fcfg.Price = cfg.Price
	st, _ := forkOK(t, store, fcfg, 2, turn(textTurn("x")))
	for i, ev := range readAll(t, store, "f1")[:13] {
		if ev.GetCost() != nil {
			t.Fatalf("copied event %d has cost %v", i+1, ev.GetCost())
		}
	}
	if st.Usage.USDMicros != 1_000_000 {
		t.Fatalf("usd = %d, want 1000000", st.Usage.USDMicros)
	}
}

func TestForkStepBudgetCountsCopiedSteps(t *testing.T) {
	store, _ := forkOrigin(t)
	cfg := forkCfg()
	cfg.Budget = &adlcv1.Budget{MaxSteps: 2}
	st, fake := forkOK(t, store, cfg, 2, textTurn("never"))
	wantStop(t, st, adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED, st.StopDetail)
	if st.ExhaustedBudget != adlcv1.BudgetKind_BUDGET_KIND_STEPS || fake.Remaining() != 1 {
		t.Fatalf("budget %v, remaining %d", st.ExhaustedBudget, fake.Remaining())
	}
}

func TestForkedRunCanBeResumed(t *testing.T) {
	store, _ := forkOrigin(t)
	deps := defaultDeps(t)
	deps.Log, deps.Model = &failingLog{Log: store, n: 7 + 1}, model.NewFake(textTurn("x"))
	// run_started + 6 copied events = 7 appends, then model_call_started.
	if _, err := Fork(context.Background(), forkCfg(), deps, ForkPoint{RunID: "r1", AtStep: 1}); !errors.Is(err, errLogDown) {
		t.Fatalf("Fork err = %v", err)
	}
	got := readAll(t, store, "f1")
	if len(got) != 8 || got[7].GetModelCallStarted() == nil {
		t.Fatalf("fork log: %v", kinds(got))
	}
	r := resumeOK(t, store, forkCfg(), defaultDeps(t), textTurn("resumed"))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_COMPLETED, "")
}
