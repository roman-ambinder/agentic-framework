package controlplane

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

func fnTool(name string, fn toolrouter.Func) toolrouter.StaticTool {
	return toolrouter.StaticTool{
		Tool: toolrouter.Tool{Spec: model.ToolSpec{Name: name}, Server: "x", Risk: adlcv1.RiskClass_RISK_CLASS_READ},
		Fn:   fn,
	}
}

func callTurn(names ...string) model.FakeTurn {
	parts := make([]model.Part, len(names))
	for i, n := range names {
		parts[i] = model.ToolCallPart(model.ToolCall{ID: "c" + n, Name: n, Arguments: json.RawMessage(`{}`)})
	}
	return model.FakeTurn{Response: &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Parts: parts},
		Finish:  adlcv1.FinishReason_FINISH_REASON_TOOL_USE,
	}}
}

func finalTurn() model.FakeTurn {
	return model.FakeTurn{Response: &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.TextPart("done")}},
		Finish:  adlcv1.FinishReason_FINISH_REASON_END_TURN,
	}}
}

// demoRun runs the harness with runner as kill switch.
func demoRun(t *testing.T, runner *Store, tools []toolrouter.StaticTool, turns ...model.FakeTurn) (*eventlog.RunState, []*adlcv1.Event, *model.Fake) {
	t.Helper()
	ctx := context.Background()
	log, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	router, err := toolrouter.NewStatic(tools...)
	if err != nil {
		t.Fatal(err)
	}
	fake := model.NewFake(turns...)
	cfg := harness.Config{RunID: "r1", AgentID: "hello@1", AgentVersion: "hello@1", Task: "go", Model: "m"}
	st, err := harness.Run(ctx, cfg, harness.Deps{
		Log: log, Model: fake, Tools: router, Policy: policy.AllowAll{}, KillSwitch: runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	evs, err := log.Read(ctx, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	return st, evs, fake
}

func twoStores(t *testing.T) (runner, operator *Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.db")
	var err error
	if runner, err = Open(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	if operator, err = Open(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operator.Close() })
	return runner, operator
}

func kindsOf(evs []*adlcv1.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = eventlog.Kind(e)
	}
	return out
}

func TestKillFromSecondStoreStopsRunWithinOneStep(t *testing.T) {
	runner, operator := twoStores(t)
	kick := fnTool("t__kick", func(ctx context.Context, _ json.RawMessage) (toolrouter.Result, error) {
		return toolrouter.Result{Output: []byte("ok")}, operator.Kill(ctx, ScopeAgent, "hello", "demo")
	})
	st, evs, fake := demoRun(t, runner, []toolrouter.StaticTool{kick}, callTurn("t__kick"), callTurn("t__kick"), finalTurn())
	want := []string{
		"run_started", "model_call_started", "model_call_completed", "tool_call_requested",
		"policy_decided", "tool_call_completed", "checkpoint", "run_completed",
	}
	if got := kindsOf(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds = %v\nwant    %v", got, want)
	}
	if st.StopReason != adlcv1.StopReason_STOP_REASON_CANCELLED || st.StopDetail != "killed: demo" {
		t.Fatalf("stop = %s %q", st.StopReason, st.StopDetail)
	}
	if fake.Remaining() != 2 {
		t.Fatalf("Remaining = %d, want 2", fake.Remaining())
	}
}

func TestKillFromSecondStoreStopsBeforeNextToolCall(t *testing.T) {
	runner, operator := twoStores(t)
	echoed := 0
	kick := fnTool("t__kick", func(ctx context.Context, _ json.RawMessage) (toolrouter.Result, error) {
		return toolrouter.Result{Output: []byte("ok")}, operator.Kill(ctx, ScopeAgent, "hello", "demo")
	})
	echo := fnTool("t__echo", func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		echoed++
		return toolrouter.Result{Output: []byte("echo")}, nil
	})
	st, evs, _ := demoRun(t, runner, []toolrouter.StaticTool{kick, echo}, callTurn("t__kick", "t__echo"), finalTurn())
	if echoed != 0 {
		t.Fatalf("t__echo ran %d times", echoed)
	}
	for _, c := range evs {
		if r := c.GetToolCallRequested(); r.GetCall().GetName() == "t__echo" {
			t.Fatal("t__echo has tool_call_requested")
		}
	}
	if st.StopReason != adlcv1.StopReason_STOP_REASON_CANCELLED || st.StopDetail != "killed: demo" {
		t.Fatalf("stop = %s %q", st.StopReason, st.StopDetail)
	}
}

func TestUnkillLetsNewRunsStart(t *testing.T) {
	runner, operator := twoStores(t)
	ctx := context.Background()
	if err := operator.Kill(ctx, ScopeAll, "", "freeze"); err != nil {
		t.Fatal(err)
	}
	if err := operator.Unkill(ctx, ScopeAll, ""); err != nil {
		t.Fatal(err)
	}
	st, _, _ := demoRun(t, runner, []toolrouter.StaticTool{fnTool("t__x", func(context.Context, json.RawMessage) (toolrouter.Result, error) { return toolrouter.Result{}, nil })}, finalTurn())
	if st.StopReason != adlcv1.StopReason_STOP_REASON_COMPLETED {
		t.Fatalf("stop = %s", st.StopReason)
	}
}
