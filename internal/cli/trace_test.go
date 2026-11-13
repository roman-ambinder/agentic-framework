package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

const traceGolden = `run r1  [completed]  +0s  6s  in=150 out=30 $0.000750
├─ model m1  [tool_use]  +1s  1s  fake/fake-1 in=100 out=20 $0.000500
├─ tool t1 fs__read_file  [ok]  +2s  1s  read allow
│    · policy +2s adlc.decision=DECISION_ALLOW adlc.rule_id=r-read adlc.reason= adlc.source=DECISION_SOURCE_LOCAL
└─ model m2  [end_turn]  +4s  1s  fake/fake-1 in=50 out=10 $0.000250
`

// seedTraceDB writes the brief's example run (r1) into a new database.
func seedTraceDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.db")
	ctx := context.Background()
	store, err := eventlog.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t0 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	mk := func(off time.Duration, cost *adlcv1.Cost, p func(*adlcv1.Event)) {
		e := &adlcv1.Event{RunId: "r1", AgentId: "coder@1", Time: timestamppb.New(t0.Add(off)), Cost: cost}
		p(e)
		if _, err := store.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	s := time.Second
	mk(0, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{AgentVersion: "coder@1", Task: "x"}}
	})
	mk(1*s, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{CallId: "m1", Step: 1, Provider: "fake", Model: "fake-1", Attempt: 1}}
	})
	mk(2*s, &adlcv1.Cost{InputTokens: 100, OutputTokens: 20, UsdMicros: 500}, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: &adlcv1.ModelCallCompleted{CallId: "m1", FinishReason: adlcv1.FinishReason_FINISH_REASON_TOOL_USE}}
	})
	mk(2*s, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ToolCallRequested{ToolCallRequested: &adlcv1.ToolCallRequested{
			Call: &adlcv1.ToolCall{Id: "t1", Name: "fs__read_file"}, RiskClass: adlcv1.RiskClass_RISK_CLASS_READ, Server: "fs",
		}}
	})
	mk(2*s, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_PolicyDecided{PolicyDecided: &adlcv1.PolicyDecided{
			CallId: "t1", Decision: adlcv1.Decision_DECISION_ALLOW, RuleId: "r-read", Source: adlcv1.DecisionSource_DECISION_SOURCE_LOCAL,
		}}
	})
	mk(3*s, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{CallId: "t1", Output: []byte("hi")}}
	})
	mk(4*s, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{CallId: "m2", Step: 2, Provider: "fake", Model: "fake-1", Attempt: 1}}
	})
	mk(5*s, &adlcv1.Cost{InputTokens: 50, OutputTokens: 10, UsdMicros: 250}, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: &adlcv1.ModelCallCompleted{CallId: "m2", FinishReason: adlcv1.FinishReason_FINISH_REASON_END_TURN}}
	})
	mk(6*s, nil, func(e *adlcv1.Event) {
		e.Payload = &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{StopReason: adlcv1.StopReason_STOP_REASON_COMPLETED}}
	})
	return path
}

func TestTraceCommandTree(t *testing.T) {
	db := seedTraceDB(t)
	code, out, errOut := run(t, "trace", "r1", "--db", db)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, errOut)
	}
	if out != traceGolden {
		t.Errorf("stdout mismatch\n got:\n%s\nwant:\n%s", out, traceGolden)
	}
}

func TestTraceCommandOTel(t *testing.T) {
	db := seedTraceDB(t)
	code, out, errOut := run(t, "trace", "r1", "--db", db, "--otel-stdout")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, `"Name": "run r1"`) {
		t.Errorf("stdout lacks the run span:\n%s", out)
	}
}

func TestTraceCommandUnknownRun(t *testing.T) {
	db := seedTraceDB(t)
	code, _, errOut := run(t, "trace", "nope", "--db", db)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "not found") {
		t.Errorf("stderr %q lacks %q", errOut, "not found")
	}
}

func TestTraceCommandNeedsOneArg(t *testing.T) {
	if code, _, _ := run(t, "trace"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}
