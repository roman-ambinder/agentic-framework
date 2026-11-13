package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
)

func checkpoints(evs []*adlcv1.Event) []*adlcv1.Checkpoint {
	return pick(evs, (*adlcv1.Event).GetCheckpoint)
}

func TestCheckpointAfterEachToolStep(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(readCall("t1", "a")), toolTurn(readCall("t2", "b")), textTurn("done"))
	wantKinds(t, r.events, "run_started",
		"model_call_started", "model_call_completed", "tool_call_requested", "policy_decided", "tool_call_completed", "checkpoint",
		"model_call_started", "model_call_completed", "tool_call_requested", "policy_decided", "tool_call_completed", "checkpoint",
		"model_call_started", "model_call_completed", "run_completed")
	cps := checkpoints(r.events)
	if len(cps) != 2 || cps[0].GetStep() != 1 || cps[1].GetStep() != 2 {
		t.Fatalf("checkpoints = %v", cps)
	}
	if cps[0].GetSnapshotRef() != "" {
		t.Fatalf("snapshot_ref = %q, want empty", cps[0].GetSnapshotRef())
	}
	if r.state.LastCheckpoint == nil || r.state.LastCheckpoint.Step != 2 {
		t.Fatalf("last checkpoint = %+v", r.state.LastCheckpoint)
	}
}

func TestCheckpointDigestMatchesState(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(readCall("t1", "a"), readCall("t2", "b")), toolTurn(readCall("t3", "c")), textTurn("done"))
	if len(checkpoints(r.events)) != 2 {
		t.Fatalf("checkpoints = %d, want 2", len(checkpoints(r.events)))
	}
	if err := VerifyCheckpoints(r.events); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyCheckpointsDetectsTampering(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(readCall("t1", "a")), toolTurn(readCall("t2", "b")), textTurn("done"))
	events := make([]*adlcv1.Event, len(r.events))
	var seq uint64
	for i, ev := range r.events {
		events[i] = ev
		if cp := ev.GetCheckpoint(); cp != nil && cp.GetStep() == 2 {
			c := proto.Clone(ev).(*adlcv1.Event)
			c.GetCheckpoint().StateDigest = "sha256:00"
			events[i], seq = c, ev.GetSeq()
		}
	}
	err := VerifyCheckpoints(events)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("seq %d", seq)) {
		t.Fatalf("err = %v, want mention of seq %d", err, seq)
	}
}

// denyPolicy denies every call.
type denyPolicy struct{}

func (denyPolicy) Authorize(context.Context, policy.Request) (policy.Decision, error) {
	return policy.Decision{Decision: adlcv1.Decision_DECISION_DENY, RuleID: "no-read", Reason: "no"}, nil
}

func TestNoCheckpointWhenStepStopsRun(t *testing.T) {
	deps := Deps{Tools: staticTools(t), Policy: denyPolicy{}}
	r := runHarness(t, context.Background(), baseConfig(), deps, toolTurn(readCall("t1", "a")))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_DENIED, "rule no-read: no")
	if n := len(checkpoints(r.events)); n != 0 {
		t.Fatalf("%d checkpoints, want 0", n)
	}
}
