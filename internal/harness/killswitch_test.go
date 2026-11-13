package harness

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

const stopCancelled = adlcv1.StopReason_STOP_REASON_CANCELLED

// fakeKillSwitch kills from the killAt-th call on (0 = never) and records its arguments.
type fakeKillSwitch struct {
	mu               sync.Mutex
	killAt, calls    int
	reason           string
	err              error // returned by every call when set
	runIDs, agentIDs []string
}

func (f *fakeKillSwitch) Killed(_ context.Context, runID, agentID string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.runIDs = append(f.runIDs, runID)
	f.agentIDs = append(f.agentIDs, agentID)
	if f.err != nil {
		return false, "", f.err
	}
	if f.killAt > 0 && f.calls >= f.killAt {
		return true, f.reason, nil
	}
	return false, "", nil
}

func killDeps(t *testing.T, ks KillSwitch) Deps {
	deps := defaultDeps(t)
	deps.KillSwitch = ks
	return deps
}

func TestKillSwitchNotKilledChangesNothing(t *testing.T) {
	base := runHarness(t, context.Background(), baseConfig(), killDeps(t, nil), twoReads()...)
	fake := &fakeKillSwitch{}
	r := runHarness(t, context.Background(), baseConfig(), killDeps(t, fake), twoReads()...)
	if !reflect.DeepEqual(kinds(r.events), kinds(base.events)) {
		t.Fatalf("kinds = %v, want %v", kinds(r.events), kinds(base.events))
	}
	wantStop(t, r.state, stopCompleted, "")
	if fake.calls != 4 {
		t.Fatalf("calls = %d, want 4", fake.calls)
	}
}

func TestKilledBeforeFirstModelCall(t *testing.T) {
	fake := &fakeKillSwitch{killAt: 1, reason: "stop now"}
	r := runHarness(t, context.Background(), baseConfig(), killDeps(t, fake), twoReads()...)
	wantKinds(t, r.events, "run_started", "run_completed")
	wantStop(t, r.state, stopCancelled, "killed: stop now")
	if r.model.Remaining() != 2 {
		t.Fatalf("Remaining = %d, want 2", r.model.Remaining())
	}
}

func TestKilledBeforeToolCall(t *testing.T) {
	fake := &fakeKillSwitch{killAt: 3, reason: "why"}
	r := runHarness(t, context.Background(), baseConfig(), killDeps(t, fake), twoReads()...)
	wantStop(t, r.state, stopCancelled, "killed: why")
	var requested, completed []string
	for _, c := range pick(r.events, (*adlcv1.Event).GetToolCallRequested) {
		requested = append(requested, c.GetCall().GetId())
	}
	for _, c := range pick(r.events, (*adlcv1.Event).GetToolCallCompleted) {
		completed = append(completed, c.GetCallId())
	}
	if !reflect.DeepEqual(requested, []string{"t1"}) || !reflect.DeepEqual(completed, []string{"t1"}) {
		t.Fatalf("requested %v completed %v, want only t1", requested, completed)
	}
	if last := kinds(r.events); last[len(last)-1] != "run_completed" {
		t.Fatalf("kinds = %v", last)
	}
	if n := len(pick(r.events, (*adlcv1.Event).GetCheckpoint)); n != 0 {
		t.Fatalf("%d checkpoints, want 0", n)
	}
}

func TestKilledBeforeNextStep(t *testing.T) {
	fake := &fakeKillSwitch{killAt: 4, reason: "later"}
	r := runHarness(t, context.Background(), baseConfig(), killDeps(t, fake), twoReads()...)
	wantStop(t, r.state, stopCancelled, "killed: later")
	if n := len(pick(r.events, (*adlcv1.Event).GetCheckpoint)); n != 1 {
		t.Fatalf("%d checkpoints, want 1", n)
	}
	if n := len(pick(r.events, (*adlcv1.Event).GetModelCallCompleted)); n != 1 {
		t.Fatalf("%d model_call_completed, want 1", n)
	}
	if last := kinds(r.events); last[len(last)-1] != "run_completed" {
		t.Fatalf("kinds = %v", last)
	}
}

func TestKilledBeforeModelRetry(t *testing.T) {
	retry := model.FakeTurn{Err: &model.Error{
		Class: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, HTTPStatus: 429, Retryable: true, Err: errors.New("slow"),
	}}
	deps := killDeps(t, &fakeKillSwitch{killAt: 2, reason: "r"})
	deps.Sleep = func(context.Context, time.Duration) error { return nil }
	r := runHarness(t, context.Background(), baseConfig(), deps, retry, textTurn("never"))
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_failed", "run_completed")
	wantStop(t, r.state, stopCancelled, "killed: r")
}

func TestKillSwitchGetsRunAndAgentID(t *testing.T) {
	fake := &fakeKillSwitch{}
	runHarness(t, context.Background(), baseConfig(), killDeps(t, fake), twoReads()...)
	if len(fake.runIDs) == 0 {
		t.Fatal("kill switch never called")
	}
	for i := range fake.runIDs {
		if fake.runIDs[i] != "r1" || fake.agentIDs[i] != "test@1" {
			t.Fatalf("call %d: run %q agent %q", i, fake.runIDs[i], fake.agentIDs[i])
		}
	}
}

func TestKillSwitchErrorIsRecordedAndRunContinues(t *testing.T) {
	fake := &fakeKillSwitch{err: errors.New("db locked")}
	r := runHarness(t, context.Background(), baseConfig(), killDeps(t, fake), twoReads()...)
	wantStop(t, r.state, stopCompleted, "")
	errs := pick(r.events, (*adlcv1.Event).GetErrorOccurred)
	if len(errs) != 4 {
		t.Fatalf("%d error_occurred, want 4", len(errs))
	}
	for _, e := range errs {
		if e.GetComponent() != "killswitch" || e.GetMessage() != "db locked" ||
			e.GetErrorClass() != adlcv1.ErrorClass_ERROR_CLASS_INTERNAL {
			t.Fatalf("error_occurred = %v", e)
		}
	}
	if r.state.Errors != 4 {
		t.Fatalf("state.Errors = %d, want 4", r.state.Errors)
	}
}

func TestResumeChecksKillSwitch(t *testing.T) {
	store := crashRun(t, 2, baseConfig(), resumeDeps(t), twoReads()...)
	fake := &fakeKillSwitch{killAt: 1, reason: "x"}
	r := resumeOK(t, store, baseConfig(), killDeps(t, fake), twoReads()...)
	wantKinds(t, r.tail, "run_resumed", "run_completed")
	wantStop(t, r.state, stopCancelled, "killed: x")
	if r.model.Remaining() != 2 {
		t.Fatalf("Remaining = %d, want 2", r.model.Remaining())
	}
}
