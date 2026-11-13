package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// sized is a transcript with one step per entry of sizes: the task, then per
// step an assistant message with a tool call (ID "t<i>") and a user message
// whose result has sizes[i] bytes.
func sized(sizes ...int) *model.Request {
	msgs := []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.TextPart("fix the bug")}}}
	for i, n := range sizes {
		id := fmt.Sprintf("t%d", i)
		msgs = append(msgs,
			model.Message{Role: model.RoleAssistant, Parts: []model.Part{
				model.ToolCallPart(model.ToolCall{ID: id, Name: "fs__read", Arguments: json.RawMessage(`{}`)}),
			}},
			model.Message{Role: model.RoleUser, Parts: []model.Part{
				model.ToolResultPart(model.ToolResult{CallID: id, Content: strings.Repeat("x", n)}),
			}},
		)
	}
	return &model.Request{Model: "m", System: "sys", Messages: msgs}
}

// sixSteps returns the output sizes of six steps of 4000 bytes (about 1000
// tokens each).
func sixSteps() []int {
	return []int{4000, 4000, 4000, 4000, 4000, 4000}
}

func TestLowWaterElidesBatch(t *testing.T) {
	req := sized(sixSteps()...)
	b := Budget{LowWaterRatio: 0.5, MaxInputTokens: EstimateRequest(req) - 1}
	got, st, err := Build(context.Background(), req, b)
	if err != nil {
		t.Fatal(err)
	}
	if limit := b.MaxInputTokens / 2; st.EstimatedTokens > limit || EstimateRequest(got) != st.EstimatedTokens {
		t.Errorf("estimate = %d (stats %d), want at most %d", EstimateRequest(got), st.EstimatedTokens, limit)
	}
	if st.ElidedResults <= 1 {
		t.Errorf("ElidedResults = %d, want more than one: the batch goes down to the low-water mark", st.ElidedResults)
	}
}

func TestLowWaterValidation(t *testing.T) {
	req := sized(sixSteps()...)
	tests := []struct {
		name  string
		ratio float64
		err   string
	}{
		{"above one", 1.5, "contextmgr: LowWaterRatio 1.5 must be in (0, 1]"},
		{"negative", -0.1, "contextmgr: LowWaterRatio -0.1 must be in (0, 1]"},
		{"not a number", math.NaN(), "contextmgr: LowWaterRatio NaN must be in (0, 1]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Build(context.Background(), req, Budget{LowWaterRatio: tt.ratio, MaxInputTokens: 1000})
			if err == nil || err.Error() != tt.err {
				t.Fatalf("err = %v, want %q", err, tt.err)
			}
		})
	}
	t.Run("zero means 0.7", func(t *testing.T) {
		b := Budget{MaxInputTokens: EstimateRequest(req) - 1}
		zero, zst, err := Build(context.Background(), req, b)
		if err != nil {
			t.Fatal(err)
		}
		b.LowWaterRatio = 0.7
		explicit, est, err := Build(context.Background(), req, b)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(zero, explicit) || zst != est {
			t.Errorf("0 gives %+v, 0.7 gives %+v", zst, est)
		}
	})
}

// memoryScenario returns a manager whose budget makes the first step elide
// exactly one result, and the requests of step 1 and of step 2. Step 2 is the
// same transcript plus one tiny step, so it fits once t0 stays elided.
func memoryScenario(t *testing.T) (*Manager, *model.Request, *model.Request) {
	t.Helper()
	req1 := sized(sixSteps()...)
	req2 := sized(append(sixSteps(), 10)...)
	b := Budget{LowWaterRatio: 1, MaxInputTokens: EstimateRequest(req1) - 1}
	return NewManager(b), req1, req2
}

func TestManagerRemembersElisions(t *testing.T) {
	m, req1, req2 := memoryScenario(t)
	out1, st1, err := m.Prepare(context.Background(), req1)
	if err != nil {
		t.Fatal(err)
	}
	if st1.ElidedResults != 1 || resultContent(out1, 0) != placeholder {
		t.Fatalf("step 1: stats %+v, t0 = %.40q", st1, resultContent(out1, 0))
	}
	out2, st2, err := m.Prepare(context.Background(), req2)
	if err != nil {
		t.Fatal(err)
	}
	if st2.ElidedResults != 0 {
		t.Errorf("step 2: ElidedResults = %d, want 0", st2.ElidedResults)
	}
	if resultContent(out2, 0) != placeholder {
		t.Errorf("step 2: t0 = %.40q, want it still elided", resultContent(out2, 0))
	}
	if resultContent(out2, 1) != strings.Repeat("x", 4000) {
		t.Error("step 2: t1 should not be elided")
	}
}

func TestManagerPlaceholderStable(t *testing.T) {
	m, req1, req2 := memoryScenario(t)
	out1, _, err := m.Prepare(context.Background(), req1)
	if err != nil {
		t.Fatal(err)
	}
	out2, _, err := m.Prepare(context.Background(), req2)
	if err != nil {
		t.Fatal(err)
	}
	b1, err1 := json.Marshal(out1.Messages[2])
	b2, err2 := json.Marshal(out2.Messages[2])
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if string(b1) != string(b2) {
		t.Errorf("elided message differs between steps:\n%s\n%s", b1, b2)
	}
}

// TestManagerElidesRarely: each step adds a 4000-byte result (about 1000
// tokens) and the budget is 12000 tokens. With Build from 1.6.1 alone and a
// low-water ratio of 1, every step after the first elision would elide one
// more result (the estimate is over budget again each time), so eliding would
// happen on about every step. With the low-water mark the elided batch leaves
// room for several steps, so elision happens only a few times in 20 steps.
func TestManagerElidesRarely(t *testing.T) {
	m := NewManager(Budget{MaxInputTokens: 12000, LowWaterRatio: 0.7, KeepRecentSteps: 2})
	var sizes []int
	elidingSteps := 0
	for i := 0; i < 20; i++ {
		sizes = append(sizes, 4000)
		_, st, err := m.Prepare(context.Background(), sized(sizes...))
		if err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
		if st.ElidedResults > 0 {
			elidingSteps++
		}
	}
	if elidingSteps == 0 || elidingSteps > 5 {
		t.Errorf("steps with new elisions = %d, want 1 to 5", elidingSteps)
	}
}

// setCallID gives step (0-based, as in sized) the tool call ID id, on both
// its call and its result.
func setCallID(req *model.Request, step int, id string) {
	req.Messages[1+2*step].Parts[0].ToolCall.ID = id
	req.Messages[2+2*step].Parts[0].ToolResult.CallID = id
}

// TestManagerKeysElisionsByPosition: providers do not guarantee unique call
// IDs across a run. Steps 0 and 1 share the ID "dup". Only the older result
// (step 0) is elided; the newer one must stay intact on the next Prepare.
func TestManagerKeysElisionsByPosition(t *testing.T) {
	req1 := sized(sixSteps()...)
	req2 := sized(append(sixSteps(), 10)...)
	for _, r := range []*model.Request{req1, req2} {
		setCallID(r, 0, "dup")
		setCallID(r, 1, "dup")
	}
	m := NewManager(Budget{LowWaterRatio: 1, MaxInputTokens: EstimateRequest(req1) - 1})
	out1, st1, err := m.Prepare(context.Background(), req1)
	if err != nil {
		t.Fatal(err)
	}
	if st1.ElidedResults != 1 || resultContent(out1, 0) != placeholder {
		t.Fatalf("step 1: stats %+v, step 0 = %.40q; want only step 0 elided", st1, resultContent(out1, 0))
	}
	out2, st2, err := m.Prepare(context.Background(), req2)
	if err != nil {
		t.Fatal(err)
	}
	if st2.ElidedResults != 0 {
		t.Errorf("step 2: ElidedResults = %d, want 0", st2.ElidedResults)
	}
	if resultContent(out2, 0) != placeholder {
		t.Errorf("step 2: step 0 = %.40q, want it still elided", resultContent(out2, 0))
	}
	if got := resultContent(out2, 1); got != strings.Repeat("x", 4000) {
		t.Errorf("step 2: step 1 = %.40q, want it intact (same CallID as the elided step 0)", got)
	}
}
