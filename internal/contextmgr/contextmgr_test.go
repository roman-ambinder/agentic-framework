package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// transcript makes message 0 = the task, then per step an assistant message
// with one tool call (ID "t<i>") and a user message with its result.
func transcript(steps, outputBytes int) *model.Request {
	msgs := []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.TextPart("fix the bug")}}}
	for i := 0; i < steps; i++ {
		id := fmt.Sprintf("t%d", i)
		msgs = append(msgs,
			model.Message{Role: model.RoleAssistant, Parts: []model.Part{
				model.ToolCallPart(model.ToolCall{ID: id, Name: "fs__read", Arguments: json.RawMessage(`{}`)}),
			}},
			model.Message{Role: model.RoleUser, Parts: []model.Part{
				model.ToolResultPart(model.ToolResult{CallID: id, Content: strings.Repeat("x", outputBytes)}),
			}},
		)
	}
	return &model.Request{Model: "m", System: "sys", Messages: msgs}
}

func resultContent(r *model.Request, step int) string {
	return r.Messages[2+2*step].Parts[0].ToolResult.Content
}

// placeholder is the exact text for a 4000-byte result.
const placeholder = "[tool output elided: 4000 bytes; call the tool again if you need it]"

func TestBuildNoLimit(t *testing.T) {
	req := transcript(3, 400)
	got, st, err := Build(context.Background(), req, Budget{})
	if err != nil || st.ElidedResults != 0 {
		t.Fatalf("err=%v stats=%+v", err, st)
	}
	if !reflect.DeepEqual(got, req) {
		t.Error("output differs from input")
	}
	if st.EstimatedTokens != EstimateRequest(req) {
		t.Errorf("EstimatedTokens = %d", st.EstimatedTokens)
	}
}

func TestBuildFitsUnchanged(t *testing.T) {
	req := transcript(3, 400)
	got, st, err := Build(context.Background(), req, Budget{MaxInputTokens: EstimateRequest(req) + 1})
	if err != nil || st.ElidedResults != 0 || !reflect.DeepEqual(got, req) {
		t.Fatalf("err=%v stats=%+v", err, st)
	}
}

// budgetEliding returns a budget that needs exactly n old results elided in
// a transcript of 6 steps of 4000 bytes.
func budgetEliding(req *model.Request, n int) int64 {
	saved := EstimateTokens(strings.Repeat("x", 4000)) - EstimateTokens(placeholder)
	return EstimateRequest(req) - int64(n)*saved
}

func TestBuildElidesOldestFirst(t *testing.T) {
	req := transcript(6, 4000)
	got, st, err := Build(context.Background(), req, Budget{MaxInputTokens: budgetEliding(req, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if st.ElidedResults != 2 {
		t.Fatalf("ElidedResults = %d", st.ElidedResults)
	}
	for i := 0; i < 6; i++ {
		want := strings.Repeat("x", 4000)
		if i < 2 {
			want = placeholder
		}
		if resultContent(got, i) != want {
			t.Errorf("t%d content = %.40q", i, resultContent(got, i))
		}
	}
}

func TestBuildPlaceholderText(t *testing.T) {
	req := transcript(6, 4000)
	got, _, err := Build(context.Background(), req, Budget{MaxInputTokens: budgetEliding(req, 1)})
	if err != nil {
		t.Fatal(err)
	}
	const want = "[tool output elided: 4000 bytes; call the tool again if you need it]"
	if placeholder != want || resultContent(got, 0) != want {
		t.Errorf("content = %q", resultContent(got, 0))
	}
}

func TestBuildKeepsProtectedWindow(t *testing.T) {
	req := transcript(6, 4000)
	got, st, err := Build(context.Background(), req, Budget{MaxInputTokens: 10})
	if !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if st.ElidedResults != 4 {
		t.Errorf("ElidedResults = %d, want 4", st.ElidedResults)
	}
	// message 0 and the last two steps (messages 9..12) are untouched.
	if !reflect.DeepEqual(got.Messages[0], req.Messages[0]) {
		t.Error("task message changed")
	}
	for i := 9; i < len(req.Messages); i++ {
		if !reflect.DeepEqual(got.Messages[i], req.Messages[i]) {
			t.Errorf("protected message %d changed", i)
		}
	}
	if resultContent(got, 3) != placeholder {
		t.Error("t3 should be elided")
	}
}

func TestBuildKeepRecentStepsSetting(t *testing.T) {
	req := transcript(6, 4000)
	got, st, err := Build(context.Background(), req, Budget{MaxInputTokens: 10, KeepRecentSteps: 4})
	if !errors.Is(err, ErrContextTooLarge) || st.ElidedResults != 2 {
		t.Fatalf("err=%v stats=%+v", err, st)
	}
	if resultContent(got, 2) != strings.Repeat("x", 4000) {
		t.Error("t2 is in the protected window")
	}
}

func TestBuildKeepsPairing(t *testing.T) {
	req := transcript(6, 4000)
	req.Messages[2].Parts[0].ToolResult.IsError = true
	got, _, err := Build(context.Background(), req, Budget{MaxInputTokens: budgetEliding(req, 3)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != len(req.Messages) {
		t.Fatal("message count changed")
	}
	for i, m := range got.Messages {
		if len(m.Parts) != len(req.Messages[i].Parts) || m.Role != req.Messages[i].Role {
			t.Fatalf("message %d shape changed", i)
		}
		for j, p := range m.Parts {
			if p.Kind != req.Messages[i].Parts[j].Kind {
				t.Fatalf("part kind changed at %d/%d", i, j)
			}
			if p.Kind != model.PartToolResult {
				continue
			}
			prev := got.Messages[i-1].Parts[j].ToolCall
			if prev == nil || prev.ID != p.ToolResult.CallID {
				t.Errorf("result %q lost its call", p.ToolResult.CallID)
			}
			if p.ToolResult.IsError != req.Messages[i].Parts[j].ToolResult.IsError {
				t.Errorf("IsError changed at %d", i)
			}
		}
	}
}

func TestBuildDoesNotModifyInput(t *testing.T) {
	req := transcript(6, 4000)
	before := transcript(6, 4000)
	if _, _, err := Build(context.Background(), req, Budget{MaxInputTokens: budgetEliding(req, 3)}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(req, before) {
		t.Error("input was modified")
	}
}

func TestBuildDoesNotElideTwice(t *testing.T) {
	req := transcript(6, 4000)
	b := Budget{MaxInputTokens: budgetEliding(req, 2)}
	first, _, err := Build(context.Background(), req, b)
	if err != nil {
		t.Fatal(err)
	}
	second, st, err := Build(context.Background(), first, b)
	if err != nil || st.ElidedResults != 0 || !reflect.DeepEqual(first, second) {
		t.Fatalf("err=%v stats=%+v", err, st)
	}
}

func TestBuildUsesCountNearLimit(t *testing.T) {
	req := transcript(6, 4000)
	max := EstimateRequest(req) // estimate fits, and is within 10% of the budget
	calls := 0
	b := Budget{MaxInputTokens: max, Count: func(_ context.Context, r *model.Request) (int64, error) {
		calls++
		if resultContent(r, 0) == placeholder {
			return max, nil // fits once one result is elided
		}
		return max + 100, nil
	}}
	got, st, err := Build(context.Background(), req, b)
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 || st.ElidedResults != 1 {
		t.Errorf("calls=%d elided=%d", calls, st.ElidedResults)
	}
	if resultContent(got, 0) != placeholder || resultContent(got, 1) != strings.Repeat("x", 4000) {
		t.Error("exactly the oldest result should be elided")
	}
}

func TestBuildCountNotCalledFarFromLimit(t *testing.T) {
	req := transcript(3, 400)
	b := Budget{MaxInputTokens: EstimateRequest(req) * 2, Count: func(context.Context, *model.Request) (int64, error) {
		t.Error("Count called")
		return 0, nil
	}}
	if _, _, err := Build(context.Background(), req, b); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCountErrorIgnored(t *testing.T) {
	req := transcript(6, 4000)
	b := Budget{MaxInputTokens: EstimateRequest(req), Count: func(context.Context, *model.Request) (int64, error) {
		return 0, errors.New("boom")
	}}
	got, st, err := Build(context.Background(), req, b)
	if err != nil || st.ElidedResults != 0 || !reflect.DeepEqual(got, req) {
		t.Fatalf("err=%v stats=%+v", err, st)
	}
}

func TestBuildCountSaysTooLargeAfterEliding(t *testing.T) {
	req := transcript(6, 4000)
	req.System = strings.Repeat("s", 400000) // keeps the estimate within 10% of the budget
	b := Budget{MaxInputTokens: EstimateRequest(req), Count: func(context.Context, *model.Request) (int64, error) {
		return 999999, nil
	}}
	_, _, err := Build(context.Background(), req, b)
	if !errors.Is(err, ErrContextTooLarge) || !strings.Contains(err.Error(), "999999") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildSkipsAlreadyElidedResults(t *testing.T) {
	req := transcript(6, 4000)
	first, _, err := Build(context.Background(), req, Budget{MaxInputTokens: budgetEliding(req, 2)})
	if err != nil {
		t.Fatal(err)
	}
	second, st, err := Build(context.Background(), first, Budget{MaxInputTokens: budgetEliding(req, 3)})
	if err != nil || st.ElidedResults != 1 {
		t.Fatalf("err=%v stats=%+v", err, st)
	}
	for i := 0; i < 3; i++ {
		if resultContent(second, i) != placeholder {
			t.Errorf("t%d = %q", i, resultContent(second, i))
		}
	}
}

// A placeholder is about 70 bytes; replacing a shorter output would make the
// request bigger, so such outputs are left alone even when over budget.
func TestBuildLeavesShortOutputs(t *testing.T) {
	req := transcript(6, 10)
	_, st, err := Build(context.Background(), req, Budget{MaxInputTokens: 1})
	if !errors.Is(err, ErrContextTooLarge) {
		t.Fatalf("err = %v, want ErrContextTooLarge", err)
	}
	if st.ElidedResults != 0 {
		t.Errorf("ElidedResults = %d, want 0: eliding 10-byte outputs only grows the request", st.ElidedResults)
	}
}
