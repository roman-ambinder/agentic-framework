package observability

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/types/known/timestamppb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

var t0 = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

// ev builds an event at t0+off. Seqs are assigned by numbered.
func ev(off time.Duration, cost *adlcv1.Cost, set func(*adlcv1.Event)) *adlcv1.Event {
	e := &adlcv1.Event{RunId: "r1", AgentId: "coder@1", Time: timestamppb.New(t0.Add(off)), Cost: cost}
	set(e)
	return e
}

func numbered(evs ...*adlcv1.Event) []*adlcv1.Event {
	for i, e := range evs {
		e.Seq = uint64(i + 1) //nolint:gosec // small index
	}
	return evs
}

func runStarted() *adlcv1.Event {
	return ev(0, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{AgentVersion: "coder@1", Task: "do it"}}
	})
}

func modelStarted(off time.Duration, id string, attempt uint32) *adlcv1.Event {
	return ev(off, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{
			CallId: id, Step: 1, Provider: "fake", Model: "fake-1", Attempt: attempt,
		}}
	})
}

func modelCompleted(off time.Duration, id string, fr adlcv1.FinishReason, in, out, usd int64) *adlcv1.Event {
	return ev(off, &adlcv1.Cost{InputTokens: in, OutputTokens: out, UsdMicros: usd}, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: &adlcv1.ModelCallCompleted{CallId: id, FinishReason: fr}}
	})
}

func modelFailed(off time.Duration, id string, retry bool) *adlcv1.Event {
	return ev(off, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ModelCallFailed{ModelCallFailed: &adlcv1.ModelCallFailed{
			CallId: id, ErrorClass: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, Message: "slow down", WillRetry: retry, HttpStatus: 429,
		}}
	})
}

func toolRequested(off time.Duration) *adlcv1.Event {
	return ev(off, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ToolCallRequested{ToolCallRequested: &adlcv1.ToolCallRequested{
			Call: &adlcv1.ToolCall{Id: "t1", Name: "fs__read_file"}, RiskClass: adlcv1.RiskClass_RISK_CLASS_READ, Server: "fs",
		}}
	})
}

func policy(off time.Duration, id string, d adlcv1.Decision, rule string) *adlcv1.Event {
	return ev(off, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_PolicyDecided{PolicyDecided: &adlcv1.PolicyDecided{
			CallId: id, Decision: d, RuleId: rule, Source: adlcv1.DecisionSource_DECISION_SOURCE_LOCAL,
		}}
	})
}

func toolCompleted(off time.Duration, id string) *adlcv1.Event {
	return ev(off, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{CallId: id, Output: []byte("hello")}}
	})
}

func runCompleted(off time.Duration, sr adlcv1.StopReason) *adlcv1.Event {
	return ev(off, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{StopReason: sr}}
	})
}

func errorOccurred(off time.Duration) *adlcv1.Event {
	return ev(off, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{Message: "boom"}}
	})
}

const sec = time.Second

// fullRun is the example run from the task brief.
func fullRun() []*adlcv1.Event {
	return numbered(
		runStarted(),
		modelStarted(1*sec, "m1", 1),
		modelCompleted(2*sec, "m1", adlcv1.FinishReason_FINISH_REASON_TOOL_USE, 100, 20, 500),
		toolRequested(2*sec),
		policy(2*sec, "t1", adlcv1.Decision_DECISION_ALLOW, "r-read"),
		toolCompleted(3*sec, "t1"),
		modelStarted(4*sec, "m2", 1),
		modelCompleted(5*sec, "m2", adlcv1.FinishReason_FINISH_REASON_END_TURN, 50, 10, 250),
		runCompleted(6*sec, adlcv1.StopReason_STOP_REASON_COMPLETED),
	)
}

func attrOf(t *testing.T, attrs []attribute.KeyValue, key string) attribute.Value {
	t.Helper()
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value
		}
	}
	t.Fatalf("attribute %q not found in %v", key, attrs)
	return attribute.Value{}
}

func TestBuildFullRun(t *testing.T) {
	root, err := Build(fullRun())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range root.Children {
		names = append(names, c.Name)
	}
	if got, want := strings.Join(names, "|"), "model m1|tool t1 fs__read_file|model m2"; got != want {
		t.Errorf("children = %q, want %q", got, want)
	}
	if got := attrOf(t, root.Children[0].Attrs, "adlc.input_tokens").AsInt64(); got != 100 {
		t.Errorf("m1 input_tokens = %d, want 100", got)
	}
	if want := t0.Add(6 * sec); !root.End.Equal(want) {
		t.Errorf("root End = %v, want %v", root.End, want)
	}
	if root.Status != "completed" {
		t.Errorf("root Status = %q, want completed", root.Status)
	}
	if got := attrOf(t, root.Attrs, "adlc.usd_micros").AsInt64(); got != 750 {
		t.Errorf("root usd_micros = %d, want 750", got)
	}
}

func TestBuildRejectsNonRun(t *testing.T) {
	tests := []struct {
		name string
		evs  []*adlcv1.Event
	}{
		{"empty", nil},
		{"starts with error", numbered(errorOccurred(0))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Build(tt.evs); !errors.Is(err, ErrNotARun) {
				t.Errorf("err = %v, want ErrNotARun", err)
			}
		})
	}
}

func TestBuildRetriedModelCall(t *testing.T) {
	root, err := Build(numbered(
		runStarted(),
		modelStarted(1*sec, "m1", 1),
		modelFailed(2*sec, "m1", true),
		modelStarted(3*sec, "m1", 2),
		modelCompleted(4*sec, "m1", adlcv1.FinishReason_FINISH_REASON_END_TURN, 1, 1, 1),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(root.Children))
	}
	first, second := root.Children[0], root.Children[1]
	if !first.Failed || !strings.HasPrefix(first.Status, "failed:") {
		t.Errorf("first: Failed=%v Status=%q", first.Failed, first.Status)
	}
	if second.Failed {
		t.Errorf("second span is failed")
	}
}

func TestBuildDeniedTool(t *testing.T) {
	root, err := Build(numbered(
		runStarted(),
		toolRequested(1*sec),
		policy(1*sec, "t1", adlcv1.Decision_DECISION_DENY, "r-deny"),
		runCompleted(2*sec, adlcv1.StopReason_STOP_REASON_DENIED),
	))
	if err != nil {
		t.Fatal(err)
	}
	tool := root.Children[0]
	if tool.Status != "denied" || !tool.Failed || tool.End.IsZero() {
		t.Errorf("tool: Status=%q Failed=%v End=%v", tool.Status, tool.Failed, tool.End)
	}
}

func TestBuildRunningRun(t *testing.T) {
	root, err := Build(numbered(runStarted(), toolRequested(1*sec)))
	if err != nil {
		t.Fatal(err)
	}
	if !root.End.IsZero() || root.Status != "running" {
		t.Errorf("root: End=%v Status=%q", root.End, root.Status)
	}
	if got := root.Children[0].Status; got != "in flight" {
		t.Errorf("tool Status = %q, want in flight", got)
	}
}

func TestBuildCompletedWithoutStart(t *testing.T) {
	tests := []struct {
		name string
		ev   *adlcv1.Event
		want string
	}{
		{"tool", toolCompleted(1*sec, "nope"), "without request"},
		{"model completed", modelCompleted(1*sec, "nope", adlcv1.FinishReason_FINISH_REASON_END_TURN, 0, 0, 0), "without start"},
		{"model failed", modelFailed(1*sec, "nope", false), "without start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Build(numbered(runStarted(), tt.ev))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuildSupersededModelCall(t *testing.T) {
	root, err := Build(numbered(runStarted(), modelStarted(1*sec, "m1", 1), modelStarted(2*sec, "m1", 2)))
	if err != nil {
		t.Fatal(err)
	}
	if got := root.Children[0].Status; got != "superseded" {
		t.Errorf("first Status = %q, want superseded", got)
	}
	if got := root.Children[1].Status; got != "in flight" {
		t.Errorf("second Status = %q, want in flight", got)
	}
}

func TestBuildErrorMarksRootFailed(t *testing.T) {
	root, err := Build(numbered(runStarted(), errorOccurred(1*sec)))
	if err != nil {
		t.Fatal(err)
	}
	if !root.Failed || len(root.Events) != 1 || root.Events[0].Name != "error" {
		t.Errorf("root: Failed=%v Events=%v", root.Failed, root.Events)
	}
}
