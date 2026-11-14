package eventlog

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// logOf builds a well-formed run: it numbers the events from 1, sets run id
// and time, and prepends run_started.
func logOf(payloads ...*adlcv1.Event) []*adlcv1.Event {
	all := append([]*adlcv1.Event{{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{
		AgentVersion: "coder@1", Task: "fix the bug",
		Budget: &adlcv1.Budget{MaxSteps: 20},
	}}}}, payloads...)
	for i, ev := range all {
		ev.RunId = "r1"
		ev.AgentId = "coder@1"
		ev.Seq = uint64(i + 1) //nolint:gosec // small test index
		ev.Time = timestamppb.New(t0.Add(time.Duration(i) * time.Second))
	}
	return all
}

func withCost(ev *adlcv1.Event, in, out, usd int64) *adlcv1.Event {
	ev.Cost = &adlcv1.Cost{InputTokens: in, OutputTokens: out, UsdMicros: usd}
	return ev
}

func modelStarted(step uint32) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{
		CallId: "m", Step: step, Attempt: 1,
	}}}
}

func modelCompleted() *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: &adlcv1.ModelCallCompleted{
		CallId: "m", FinishReason: adlcv1.FinishReason_FINISH_REASON_TOOL_USE,
	}}}
}

func modelFailed() *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ModelCallFailed{ModelCallFailed: &adlcv1.ModelCallFailed{
		CallId: "m", WillRetry: true,
	}}}
}

func toolRequested(id string) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ToolCallRequested{ToolCallRequested: &adlcv1.ToolCallRequested{
		Call: &adlcv1.ToolCall{Id: id, Name: "fs.read_file"}, RiskClass: adlcv1.RiskClass_RISK_CLASS_READ,
	}}}
}

func toolCompleted(id string, isErr bool) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{
		CallId: id, IsError: isErr,
	}}}
}

func policy(id string, d adlcv1.Decision) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_PolicyDecided{PolicyDecided: &adlcv1.PolicyDecided{
		CallId: id, Decision: d,
	}}}
}

func approvalRequested(id string) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ApprovalRequested{ApprovalRequested: &adlcv1.ApprovalRequested{
		ApprovalId: id, CallId: "t9",
	}}}
}

func approvalResolved(id string, ok bool) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ApprovalResolved{ApprovalResolved: &adlcv1.ApprovalResolved{
		ApprovalId: id, Approved: ok, ResolvedBy: "user:local",
	}}}
}

func checkpoint(step uint32) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_Checkpoint{Checkpoint: &adlcv1.Checkpoint{
		Step: step, SnapshotRef: "snap", StateDigest: "sha256:x",
	}}}
}

func resumed() *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_RunResumed{RunResumed: &adlcv1.RunResumed{NextStep: 2, Reason: "test"}}}
}

func sandboxSuspended() *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_SandboxSuspended{SandboxSuspended: &adlcv1.SandboxSuspended{
		SnapshotRef: "runs/r1/workspace.tar", WorkspaceSha256: "sha256:x", WorkspaceBytes: 10,
	}}}
}

func sandboxRestored() *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_SandboxRestored{SandboxRestored: &adlcv1.SandboxRestored{
		SuspendedSeq: 3, SandboxId: "box1",
	}}}
}

func failure() *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{Message: "boom"}}}
}

func completed(r adlcv1.StopReason) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{
		StopReason: r, Result: "done",
	}}}
}

func mustFold(t *testing.T, evs []*adlcv1.Event) *RunState {
	t.Helper()
	s, err := Fold(evs)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	return s
}

func TestFoldFullRun(t *testing.T) {
	evs := logOf(
		withCost(modelStarted(1), 0, 0, 0),
		withCost(modelCompleted(), 100, 20, 500),
		toolRequested("t1"),
		policy("t1", adlcv1.Decision_DECISION_ALLOW),
		toolCompleted("t1", false),
		toolRequested("t2"),
		toolCompleted("t2", true),
		checkpoint(1),
		modelFailed(),
		modelStarted(2),
		withCost(modelCompleted(), 50, 10, 250),
		failure(),
		completed(adlcv1.StopReason_STOP_REASON_COMPLETED),
	)
	s := mustFold(t, evs)

	want := RunState{
		RunID: "r1", AgentID: "coder@1", AgentVersion: "coder@1", Task: "fix the bug",
		StartedAt: t0, LastEventAt: t0.Add(13 * time.Second), LastSeq: 14,
		Status: StatusFinished, StopReason: adlcv1.StopReason_STOP_REASON_COMPLETED, Result: "done",
		Step: 2, ModelCalls: 2, ModelCallFailures: 1, ToolCalls: 2, ToolErrors: 1, Errors: 1,
		Usage:            Usage{InputTokens: 150, OutputTokens: 30, USDMicros: 750},
		PendingToolCalls: []string{}, PendingApprovals: nil,
		LastCheckpoint: &CheckpointRef{Seq: 9, Step: 1, SnapshotRef: "snap", StateDigest: "sha256:x"},
	}
	if !proto.Equal(s.Budget, &adlcv1.Budget{MaxSteps: 20}) {
		t.Errorf("Budget = %v", s.Budget)
	}
	s.Budget = nil
	if !reflect.DeepEqual(*s, want) {
		t.Errorf("state mismatch:\n got %+v\nwant %+v", *s, want)
	}
}

func TestFoldIsDeterministic(t *testing.T) {
	build := func() []*adlcv1.Event {
		return logOf(modelStarted(1), withCost(modelCompleted(), 10, 2, 30),
			toolRequested("t1"), toolCompleted("t1", false))
	}
	a, b := mustFold(t, build()), mustFold(t, build())
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same events gave different states:\n%+v\n%+v", a, b)
	}
	da, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	db, _ := b.Digest()
	if da != db {
		t.Errorf("digests differ: %s vs %s", da, db)
	}

	// Any change to the events must change the digest.
	changed := build()
	changed[2].Cost.InputTokens++
	dc, _ := mustFold(t, changed).Digest()
	if dc == da {
		t.Error("digest did not change when cost changed")
	}
	changedBudget := build()
	changedBudget[0].GetRunStarted().Budget.MaxSteps++
	dd, _ := mustFold(t, changedBudget).Digest()
	if dd == da {
		t.Error("digest did not change when budget changed")
	}
}

func TestIncrementalApplyMatchesFold(t *testing.T) {
	evs := logOf(modelStarted(1), toolRequested("t1"), toolCompleted("t1", false), checkpoint(1))
	var s RunState
	for _, ev := range evs {
		if err := s.Apply(ev); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(&s, mustFold(t, evs)) {
		t.Error("Apply one by one differs from Fold")
	}
}

func TestFoldPauseAndResume(t *testing.T) {
	evs := logOf(
		toolRequested("t9"),
		policy("t9", adlcv1.Decision_DECISION_NEEDS_APPROVAL),
		approvalRequested("a1"),
		completed(adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL),
	)
	s := mustFold(t, evs)
	if s.Status != StatusPaused || !slices.Equal(s.PendingApprovals, []string{"a1"}) {
		t.Fatalf("status %s pending %v; want paused [a1]", s.Status, s.PendingApprovals)
	}

	evs = logOf(
		toolRequested("t9"),
		policy("t9", adlcv1.Decision_DECISION_NEEDS_APPROVAL),
		approvalRequested("a1"),
		completed(adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL),
		approvalResolved("a1", true),
		toolCompleted("t9", false),
		completed(adlcv1.StopReason_STOP_REASON_COMPLETED),
	)
	s = mustFold(t, evs)
	if s.Status != StatusFinished || len(s.PendingApprovals) != 0 || len(s.PendingToolCalls) != 0 {
		t.Errorf("status %s approvals %v tools %v; want finished, none, none",
			s.Status, s.PendingApprovals, s.PendingToolCalls)
	}
}

func TestFoldDeniedCallIsNotPending(t *testing.T) {
	s := mustFold(t, logOf(toolRequested("t1"), policy("t1", adlcv1.Decision_DECISION_DENY)))
	if len(s.PendingToolCalls) != 0 || s.Denials != 1 {
		t.Errorf("pending %v denials %d; want none, 1", s.PendingToolCalls, s.Denials)
	}
	s = mustFold(t, logOf(approvalRequested("a2"), approvalResolved("a2", false)))
	if s.Denials != 1 {
		t.Errorf("rejected approval: denials %d, want 1", s.Denials)
	}
}

func TestFoldInvalidLogs(t *testing.T) {
	tests := []struct {
		name   string
		events func() []*adlcv1.Event
	}{
		{"empty", func() []*adlcv1.Event { return nil }},
		{"first not run_started", func() []*adlcv1.Event { return logOf(failure())[1:] }},
		{"seq gap", func() []*adlcv1.Event {
			evs := logOf(failure(), failure())
			return slices.Delete(evs, 1, 2)
		}},
		{"other run's event", func() []*adlcv1.Event {
			evs := logOf(failure())
			evs[1].RunId = "r2"
			return evs
		}},
		{"second run_started", func() []*adlcv1.Event { return logOf(logOf()[0]) }},
		{"event after finish", func() []*adlcv1.Event {
			return logOf(completed(adlcv1.StopReason_STOP_REASON_COMPLETED), failure())
		}},
		{"run_resumed after finish", func() []*adlcv1.Event {
			return logOf(completed(adlcv1.StopReason_STOP_REASON_COMPLETED), resumed())
		}},
		{"run_resumed while paused", func() []*adlcv1.Event {
			return logOf(approvalRequested("a1"), completed(adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL), resumed())
		}},
		{"paused run continues without approval", func() []*adlcv1.Event {
			return logOf(approvalRequested("a1"), completed(adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL), failure())
		}},
		{"awaiting approval with none pending", func() []*adlcv1.Event {
			return logOf(completed(adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL))
		}},
		{"completed without stop reason", func() []*adlcv1.Event {
			return logOf(completed(adlcv1.StopReason_STOP_REASON_UNSPECIFIED))
		}},
		{"tool completed but never requested", func() []*adlcv1.Event { return logOf(toolCompleted("t1", false)) }},
		{"tool requested twice", func() []*adlcv1.Event { return logOf(toolRequested("t1"), toolRequested("t1")) }},
		{"tool requested without id", func() []*adlcv1.Event { return logOf(toolRequested("")) }},
		{"approval resolved but never requested", func() []*adlcv1.Event { return logOf(approvalResolved("a1", true)) }},
		{"approval requested twice", func() []*adlcv1.Event {
			return logOf(approvalRequested("a1"), approvalRequested("a1"))
		}},
		{"approval requested without id", func() []*adlcv1.Event { return logOf(approvalRequested("")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Fold(tt.events())
			if !errors.Is(err, ErrInvalidLog) {
				t.Fatalf("err = %v, want ErrInvalidLog", err)
			}
		})
	}
}

func TestApplyLeavesStateUnchangedOnError(t *testing.T) {
	s := mustFold(t, logOf(toolRequested("t1")))
	before := *s
	before.PendingToolCalls = slices.Clone(s.PendingToolCalls)

	bad := toolRequested("t1") // duplicate id
	bad.RunId, bad.Seq = "r1", 3
	bad.Cost = &adlcv1.Cost{InputTokens: 99}
	if err := s.Apply(bad); err == nil {
		t.Fatal("Apply accepted a duplicate tool call")
	}
	if !reflect.DeepEqual(*s, before) {
		t.Errorf("state changed after failed Apply:\n got %+v\nwant %+v", *s, before)
	}
}

func TestFoldSkipsUnknownKinds(t *testing.T) {
	unknown := withCost(&adlcv1.Event{}, 5, 0, 7) // no payload this binary knows
	s := mustFold(t, logOf(unknown, failure()))
	if s.UnknownEvents != 1 || s.LastSeq != 3 || s.Usage.USDMicros != 7 {
		t.Errorf("unknown %d seq %d usd %d; want 1, 3, 7", s.UnknownEvents, s.LastSeq, s.Usage.USDMicros)
	}
}

// Every payload kind in the schema must be handled by apply, not fall into
// the unknown-kind branch. This fails when a kind is added to events.proto
// without updating fold.go.
func TestEveryKindIsHandled(t *testing.T) {
	samples := map[string]*adlcv1.Event{
		"model_call_started":   modelStarted(1),
		"model_call_completed": modelCompleted(),
		"model_call_failed":    modelFailed(),
		"tool_call_requested":  toolRequested("t1"),
		"tool_call_completed":  toolCompleted("t1", false),
		"policy_decided":       policy("t1", adlcv1.Decision_DECISION_ALLOW),
		"approval_requested":   approvalRequested("a1"),
		"approval_resolved":    approvalResolved("a1", true),
		"checkpoint":           checkpoint(1),
		"error_occurred":       failure(),
		"run_resumed":          resumed(),
		"sandbox_suspended":    sandboxSuspended(),
		"sandbox_restored":     sandboxRestored(),
		"run_completed":        completed(adlcv1.StopReason_STOP_REASON_COMPLETED),
	}
	order := []string{
		"model_call_started", "model_call_completed", "model_call_failed",
		"tool_call_requested", "policy_decided", "tool_call_completed",
		"approval_requested", "approval_resolved", "checkpoint", "error_occurred", "run_resumed",
		"sandbox_suspended", "sandbox_restored", "run_completed",
	}
	var evs []*adlcv1.Event
	for _, k := range order {
		evs = append(evs, samples[k])
	}
	evs = logOf(evs...)

	seen := map[string]bool{}
	for _, ev := range evs {
		seen[Kind(ev)] = true
	}
	fields := (&adlcv1.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("payload").Fields()
	for i := range fields.Len() {
		if name := string(fields.Get(i).Name()); !seen[name] {
			t.Errorf("payload kind %q has no sample in this test", name)
		}
	}
	if s := mustFold(t, evs); s.UnknownEvents != 0 {
		t.Errorf("%d known kinds were treated as unknown", s.UnknownEvents)
	}
}

// run_resumed must not add anything to RunState: a new field would change the
// digest of every existing log.
func TestFoldRunResumedChangesOnlyPosition(t *testing.T) {
	before := mustFold(t, logOf(modelStarted(1), failure()))
	after := *before
	evs := logOf(modelStarted(1), failure(), resumed())
	got := mustFold(t, evs)
	after.LastSeq, after.LastEventAt = got.LastSeq, got.LastEventAt
	if !reflect.DeepEqual(*got, after) {
		t.Fatalf("run_resumed changed more than seq and time:\n got %+v\nwant %+v", *got, after)
	}
	if got.Status != StatusRunning {
		t.Fatalf("status = %v, want running", got.Status)
	}
}

func awaiting() []*adlcv1.Event {
	return []*adlcv1.Event{
		approvalRequested("a1"),
		completed(adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL),
	}
}

func TestPausedRunAcceptsSandboxEvents(t *testing.T) {
	base := awaiting()
	s := mustFold(t, logOf(append(slices.Clone(base), sandboxSuspended(), sandboxRestored())...))
	if s.Status != StatusPaused || s.UnknownEvents != 0 {
		t.Fatalf("status %s unknown %d; want paused, 0", s.Status, s.UnknownEvents)
	}
	s = mustFold(t, logOf(append(slices.Clone(base), sandboxSuspended(), sandboxRestored(), approvalResolved("a1", true))...))
	if s.Status != StatusRunning || s.UnknownEvents != 0 {
		t.Fatalf("status %s unknown %d; want running, 0", s.Status, s.UnknownEvents)
	}
}

func TestRunningRunAcceptsSandboxEvents(t *testing.T) {
	s := mustFold(t, logOf(sandboxRestored()))
	if s.Status != StatusRunning || s.UnknownEvents != 0 {
		t.Fatalf("status %s unknown %d; want running, 0", s.Status, s.UnknownEvents)
	}
}

// A sandbox event may move only the seq and time, or every digest changes.
func TestSandboxEventsChangeNoStateField(t *testing.T) {
	before := mustFold(t, logOf(awaiting()...))
	after := *mustFold(t, logOf(awaiting()...))
	next := sandboxSuspended()
	next.RunId, next.Seq, next.Time = "r1", before.LastSeq+1, timestamppb.New(t0.Add(time.Hour))
	if err := after.Apply(next); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	a, b := *before, after
	a.LastSeq, a.LastEventAt = 0, time.Time{}
	b.LastSeq, b.LastEventAt = 0, time.Time{}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("sandbox_suspended changed more than seq and time:\n got %+v\nwant %+v", b, a)
	}
}

func TestPausedRunStillRejectsOtherKinds(t *testing.T) {
	tests := []struct {
		name string
		ev   *adlcv1.Event
	}{
		{"model_call_started", modelStarted(1)},
		{"tool_call_requested", toolRequested("t1")},
		{"checkpoint", checkpoint(2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Fold(logOf(append(awaiting(), tt.ev)...))
			const want = "paused run can only continue with approval_resolved or a sandbox event"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want containing %q", err, want)
			}
		})
	}
}

func TestFinishedRunRejectsSandboxEvents(t *testing.T) {
	_, err := Fold(logOf(completed(adlcv1.StopReason_STOP_REASON_COMPLETED), sandboxSuspended()))
	if err == nil || !strings.Contains(err.Error(), "run already finished") {
		t.Fatalf("err = %v, want run already finished", err)
	}
}
