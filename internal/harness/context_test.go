package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/contextmgr"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// bigTool returns n bytes of "x" for every call.
func bigTool(n int) toolrouter.StaticTool {
	return funcTool("x__big", adlcv1.RiskClass_RISK_CLASS_READ,
		func(context.Context, json.RawMessage) (toolrouter.Result, error) {
			return toolrouter.Result{Output: []byte(strings.Repeat("x", n))}, nil
		})
}

func bigCall(i int) model.ToolCall {
	return model.ToolCall{ID: fmt.Sprintf("c%d", i), Name: "x__big", Arguments: json.RawMessage(`{}`)}
}

// resultContents returns the content of every tool result in req, in order.
func resultContents(req *model.Request) []string {
	var out []string
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.ToolResult != nil {
				out = append(out, p.ToolResult.Content)
			}
		}
	}
	return out
}

func TestHarnessElidesOldToolOutput(t *testing.T) {
	cfg := baseConfig()
	cfg.Context = contextmgr.Budget{MaxInputTokens: 5000}
	deps := Deps{Tools: staticTools(t, bigTool(8000)), Policy: policy.AllowAll{}}
	r := runHarness(t, context.Background(), cfg, deps,
		toolTurn(bigCall(1)), toolTurn(bigCall(2)), toolTurn(bigCall(3)), textTurn("done"))
	wantStop(t, r.state, stopCompleted, "")

	reqs := r.model.Requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want 4", len(reqs))
	}
	got := resultContents(reqs[3])
	const placeholder = "[tool output elided: 8000 bytes; call the tool again if you need it]"
	full := strings.Repeat("x", 8000)
	if len(got) != 3 || got[0] != placeholder || got[1] != full || got[2] != full {
		t.Fatalf("4th request results = %.50q", got)
	}
	if first := resultContents(reqs[1]); first[0] != full {
		t.Error("2nd request should not be elided")
	}
	outs := pick(r.events, (*adlcv1.Event).GetToolCallCompleted)
	if len(outs) != 3 || len(outs[0].GetOutput()) != 8000 {
		t.Fatalf("event log lost the full output: %d events", len(outs))
	}
}

func TestHarnessContextTooLarge(t *testing.T) {
	cfg := baseConfig()
	cfg.Task = strings.Repeat("task ", 100)
	cfg.Context = contextmgr.Budget{MaxInputTokens: 10}
	r := runHarness(t, context.Background(), cfg, defaultDeps(t), textTurn("never"))
	wantStop(t, r.state, stopError, "context budget exceeded")
	wantKinds(t, r.events, "run_started", "error_occurred", "run_completed")
	e := r.events[1].GetErrorOccurred()
	if e.GetErrorClass() != adlcv1.ErrorClass_ERROR_CLASS_INTERNAL || !strings.Contains(e.GetMessage(), "budget") {
		t.Fatalf("error_occurred = %v", e)
	}
	if n := len(r.model.Requests()); n != 0 {
		t.Fatalf("model was called %d times", n)
	}
}

// windowModel is a fake that reports a context window.
type windowModel struct {
	*model.Fake
	window int
}

func (w windowModel) Capabilities() model.Caps {
	return model.Caps{ToolCalling: true, MaxContextTokens: w.window}
}

func TestHarnessBudgetDerivedFromCapabilities(t *testing.T) {
	cfg := baseConfig()
	cfg.Task = strings.Repeat("task ", 100) // about 125 tokens
	store := newLog(t)
	fake := model.NewFake(textTurn("never"))
	deps := Deps{Log: store, Model: windowModel{fake, 100}, Tools: staticTools(t), Policy: policy.AllowAll{}}
	state, err := Run(context.Background(), cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	wantStop(t, state, stopError, "context budget exceeded")
	if len(fake.Requests()) != 0 {
		t.Error("model should not be called")
	}
}

func TestEffectiveBudget(t *testing.T) {
	tests := []struct {
		name string
		in   contextmgr.Budget
		caps model.Caps
		want int64
	}{
		{"unknown window", contextmgr.Budget{}, model.Caps{}, 0},
		{"derived 80 percent", contextmgr.Budget{}, model.Caps{MaxContextTokens: 1000}, 800},
		{"explicit wins", contextmgr.Budget{MaxInputTokens: 5}, model.Caps{MaxContextTokens: 1000}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveBudget(tt.in, tt.caps).MaxInputTokens; got != tt.want {
				t.Errorf("MaxInputTokens = %d, want %d", got, tt.want)
			}
		})
	}
}
