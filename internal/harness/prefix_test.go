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

// mustJSON returns the JSON encoding of v; the prompt cache compares bytes.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkPrefix asserts that r2 starts with the same bytes as r1: the same tools
// and system prompt, and every message of r1 unchanged, with messages added.
func checkPrefix(t *testing.T, r1, r2 *model.Request) {
	t.Helper()
	if a, b := mustJSON(t, r1.Tools), mustJSON(t, r2.Tools); a != b {
		t.Errorf("tools differ between steps:\n%s\n%s", a, b)
	}
	if r1.System != r2.System {
		t.Errorf("system differs between steps:\n%q\n%q", r1.System, r2.System)
	}
	if len(r2.Messages) <= len(r1.Messages) {
		t.Fatalf("request has %d messages after %d", len(r2.Messages), len(r1.Messages))
	}
	for i := range r1.Messages {
		if a, b := mustJSON(t, r1.Messages[i]), mustJSON(t, r2.Messages[i]); a != b {
			t.Errorf("message %d differs between steps:\n%s\n%s", i, a, b)
		}
	}
}

// elidedCount returns how many tool results of req are placeholders.
func elidedCount(req *model.Request) int {
	n := 0
	for _, c := range resultContents(req) {
		if strings.HasPrefix(c, "[tool output elided:") {
			n++
		}
	}
	return n
}

func TestHarnessToolsSorted(t *testing.T) {
	ok := func(context.Context, json.RawMessage) (toolrouter.Result, error) {
		return toolrouter.Result{Output: []byte("ok")}, nil
	}
	router, err := toolrouter.NewStatic(funcTool("b__x", adlcv1.RiskClass_RISK_CLASS_READ, ok), funcTool("a__y", adlcv1.RiskClass_RISK_CLASS_READ, ok))
	if err != nil {
		t.Fatal(err)
	}
	call := model.ToolCall{ID: "c1", Name: "a__y", Arguments: json.RawMessage(`{}`)}
	r := runHarness(t, context.Background(), baseConfig(), Deps{Tools: router, Policy: policy.AllowAll{}},
		toolTurn(call), textTurn("done"))
	reqs := r.model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	for i, req := range reqs {
		var names []string
		for _, spec := range req.Tools {
			names = append(names, spec.Name)
		}
		if got := strings.Join(names, ","); got != "a__y,b__x" {
			t.Errorf("request %d tools = %s, want a__y,b__x", i+1, got)
		}
	}
}

func TestHarnessPrefixStable(t *testing.T) {
	var turns []model.FakeTurn
	for i := 1; i <= 5; i++ {
		turns = append(turns, toolTurn(readCall(fmt.Sprintf("c%d", i), fmt.Sprintf("f%d.txt", i))))
	}
	turns = append(turns, textTurn("done"))
	deps := Deps{Tools: staticTools(t), Policy: policy.AllowAll{}}
	r := runHarness(t, context.Background(), baseConfig(), deps, turns...)
	wantStop(t, r.state, stopCompleted, "")
	reqs := r.model.Requests()
	if len(reqs) != 6 {
		t.Fatalf("%d requests, want 6", len(reqs))
	}
	for i := 1; i < len(reqs); i++ {
		checkPrefix(t, reqs[i-1], reqs[i])
	}
}

// TestHarnessPrefixStableBetweenElisions: 4000-byte outputs are about 1000
// tokens each, so with a budget of 4000 tokens the run starts eliding at the
// sixth request. Requests after an elision batch must keep the prefix of the
// request before them, for every pair where no new result was elided.
func TestHarnessPrefixStableBetweenElisions(t *testing.T) {
	cfg := baseConfig()
	cfg.Context = contextmgr.Budget{MaxInputTokens: 4000}
	var turns []model.FakeTurn
	for i := 1; i <= 11; i++ {
		turns = append(turns, toolTurn(bigCall(i)))
	}
	turns = append(turns, textTurn("done"))
	deps := Deps{Tools: staticTools(t, bigTool(4000)), Policy: policy.AllowAll{}}
	r := runHarness(t, context.Background(), cfg, deps, turns...)
	wantStop(t, r.state, stopCompleted, "")
	reqs := r.model.Requests()
	if len(reqs) != 12 {
		t.Fatalf("%d requests, want 12", len(reqs))
	}
	first := -1
	for i, req := range reqs {
		if elidedCount(req) > 0 {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatal("the budget never forced an elision")
	}
	checked := 0
	for i := first + 1; i < len(reqs); i++ {
		if elidedCount(reqs[i]) == elidedCount(reqs[i-1]) {
			checkPrefix(t, reqs[i-1], reqs[i])
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no pair without a new elision after the first elision")
	}
}
