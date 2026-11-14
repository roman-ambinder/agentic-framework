// Package contracts_test checks the generated Go code for the
// language-neutral contracts in contracts/proto.
package contracts_test

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// samplePayloads has one example per Event payload kind. TestEveryPayloadKindCovered
// fails when a new kind is added to the schema without an entry here.
func samplePayloads() map[string]*adlcv1.Event {
	return map[string]*adlcv1.Event{
		"run_started": {Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{
			AgentVersion: "coder@1", Task: "fix bug",
			Budget: &adlcv1.Budget{MaxSteps: 50, MaxWallClock: durationpb.New(10 * time.Minute)},
		}}},
		"run_resumed": {Payload: &adlcv1.Event_RunResumed{RunResumed: &adlcv1.RunResumed{
			NextStep: 3, VerifiedCheckpointSeq: 9, AbandonedToolCalls: 1, Reason: "process stopped without run_completed",
		}}},
		"sandbox_suspended": {Payload: &adlcv1.Event_SandboxSuspended{SandboxSuspended: &adlcv1.SandboxSuspended{
			SnapshotRef: "runs/r1/workspace.tar", WorkspaceSha256: "sha256:abc", WorkspaceBytes: 2048,
			ImageId: "sha256:img", CheckpointId: "cp1", ProcessesSkipped: "",
		}}},
		"sandbox_restored": {Payload: &adlcv1.Event_SandboxRestored{SandboxRestored: &adlcv1.SandboxRestored{
			SuspendedSeq: 7, Processes: true, FallbackCause: "", SandboxId: "box1",
		}}},
		"run_completed": {Payload: &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{
			StopReason: adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED, ExhaustedBudget: adlcv1.BudgetKind_BUDGET_KIND_STEPS,
		}}},
		"model_call_started": {Payload: &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{
			CallId: "m1", Step: 1, Provider: "example-provider", Model: "example-model", Attempt: 1,
		}}},
		"model_call_completed": {Payload: &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: &adlcv1.ModelCallCompleted{
			CallId: "m1", FinishReason: adlcv1.FinishReason_FINISH_REASON_TOOL_USE,
			ToolCalls:     []*adlcv1.ToolCall{{Id: "t1", Name: "fs.read_file", ArgumentsJson: `{"path":"main.go"}`}},
			ProviderBlobs: []*adlcv1.ProviderBlob{{Provider: "example-provider", Kind: "thinking", Data: []byte{0x00, 0xff}}},
		}}},
		"model_call_failed": {Payload: &adlcv1.Event_ModelCallFailed{ModelCallFailed: &adlcv1.ModelCallFailed{
			CallId: "m1", ErrorClass: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, WillRetry: true, HttpStatus: 429,
		}}},
		"tool_call_requested": {Payload: &adlcv1.Event_ToolCallRequested{ToolCallRequested: &adlcv1.ToolCallRequested{
			Call: &adlcv1.ToolCall{Id: "t1", Name: "fs.read_file"}, RiskClass: adlcv1.RiskClass_RISK_CLASS_READ, Server: "fs",
		}}},
		"tool_call_completed": {Payload: &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{
			CallId: "t1", Output: []byte("package main"), Duration: durationpb.New(time.Millisecond),
		}}},
		"policy_decided": {Payload: &adlcv1.Event_PolicyDecided{PolicyDecided: &adlcv1.PolicyDecided{
			CallId: "t1", Decision: adlcv1.Decision_DECISION_ALLOW, Source: adlcv1.DecisionSource_DECISION_SOURCE_LOCAL,
		}}},
		"approval_requested": {Payload: &adlcv1.Event_ApprovalRequested{ApprovalRequested: &adlcv1.ApprovalRequested{
			ApprovalId: "a1", CallId: "t2", Summary: "open PR",
		}}},
		"approval_resolved": {Payload: &adlcv1.Event_ApprovalResolved{ApprovalResolved: &adlcv1.ApprovalResolved{
			ApprovalId: "a1", Approved: true, ResolvedBy: "user:local",
		}}},
		"checkpoint": {Payload: &adlcv1.Event_Checkpoint{Checkpoint: &adlcv1.Checkpoint{
			Step: 3, SnapshotRef: "snap-1", StateDigest: "sha256:abc",
		}}},
		"error_occurred": {Payload: &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{
			ErrorClass: adlcv1.ErrorClass_ERROR_CLASS_TOOL, Message: "boom", Component: "toolrouter",
		}}},
	}
}

func withEnvelope(ev *adlcv1.Event) *adlcv1.Event {
	ev.RunId = "run-1"
	ev.Seq = 7
	ev.Time = timestamppb.New(time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC))
	ev.AgentId = "coder@1"
	ev.ParentSeq = 6
	ev.Cost = &adlcv1.Cost{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 80, UsdMicros: 1234}
	ev.Trace = &adlcv1.TraceContext{Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}
	return ev
}

func TestEveryPayloadKindCovered(t *testing.T) {
	oneof := (&adlcv1.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("payload")
	samples := samplePayloads()
	fields := oneof.Fields()
	for i := range fields.Len() {
		name := string(fields.Get(i).Name())
		if _, ok := samples[name]; !ok {
			t.Errorf("payload kind %q has no sample in samplePayloads", name)
		}
	}
	if len(samples) != fields.Len() {
		t.Errorf("samplePayloads has %d entries, schema has %d payload kinds", len(samples), fields.Len())
	}
}

func TestRoundTripBinary(t *testing.T) {
	for name, ev := range samplePayloads() {
		t.Run(name, func(t *testing.T) {
			in := withEnvelope(ev)
			b, err := proto.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			out := &adlcv1.Event{}
			if err := proto.Unmarshal(b, out); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(in, out) {
				t.Errorf("binary round trip changed the event:\n in: %v\nout: %v", in, out)
			}
		})
	}
}

func TestRoundTripJSON(t *testing.T) {
	for name, ev := range samplePayloads() {
		t.Run(name, func(t *testing.T) {
			in := withEnvelope(ev)
			b, err := protojson.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			out := &adlcv1.Event{}
			if err := protojson.Unmarshal(b, out); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(in, out) {
				t.Errorf("JSON round trip changed the event:\n in: %s", b)
			}
		})
	}
}

// An older reader must be able to decode an event written by a newer writer
// that has a payload kind the reader doesn't know.
func TestUnknownPayloadKindIsTolerated(t *testing.T) {
	b, err := proto.Marshal(withEnvelope(&adlcv1.Event{}))
	if err != nil {
		t.Fatal(err)
	}
	const futureField protowire.Number = 999
	b = protowire.AppendTag(b, futureField, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte{0x0a, 0x01, 'x'})

	out := &adlcv1.Event{}
	if err := proto.Unmarshal(b, out); err != nil {
		t.Fatalf("decoding an unknown payload kind failed: %v", err)
	}
	if out.GetPayload() != nil {
		t.Errorf("payload = %T, want nil for unknown kind", out.GetPayload())
	}
	if out.GetRunId() != "run-1" || out.GetSeq() != 7 {
		t.Errorf("envelope lost: run_id=%q seq=%d", out.GetRunId(), out.GetSeq())
	}
	if len(out.ProtoReflect().GetUnknown()) == 0 {
		t.Error("unknown field was dropped; it must be kept so re-encoding preserves it")
	}
}

func TestEnumZeroValuesAreUnspecified(t *testing.T) {
	enums := adlcv1.File_adlc_v1_events_proto.Enums()
	for i := range enums.Len() {
		e := enums.Get(i)
		zero := e.Values().ByNumber(0)
		if zero == nil || !strings.HasSuffix(string(zero.Name()), "_UNSPECIFIED") {
			t.Errorf("enum %s: zero value must be *_UNSPECIFIED", e.Name())
		}
	}
}

func TestPluginMessagesRoundTrip(t *testing.T) {
	info := &adlcv1.ToolCallInfo{
		Call:      &adlcv1.ToolCall{Id: "t1", Name: "fs__read_file", ArgumentsJson: `{"path":"main.go"}`},
		RiskClass: adlcv1.RiskClass_RISK_CLASS_WRITE_EXTERNAL,
		Server:    "fs",
	}
	tests := []struct {
		name string
		in   proto.Message
		out  proto.Message
	}{
		{"pre_tool_request", &adlcv1.PreToolRequest{Info: info}, &adlcv1.PreToolRequest{}},
		{"pre_tool_response", &adlcv1.PreToolResponse{Decision: adlcv1.Decision_DECISION_DENY, Reason: "no"}, &adlcv1.PreToolResponse{}},
		{"post_tool_request", &adlcv1.PostToolRequest{
			Info: info, IsError: true, Output: []byte{0x00, 0xff, 'x'}, ExitCode: -2,
			Duration: durationpb.New(1500 * time.Millisecond),
		}, &adlcv1.PostToolRequest{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := proto.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if err := proto.Unmarshal(b, tt.out); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(tt.in, tt.out) {
				t.Errorf("round trip changed the message:\n in: %v\nout: %v", tt.in, tt.out)
			}
		})
	}
}
