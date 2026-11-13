package harness

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// testHook is a Hook that records the tool names it saw.
type testHook struct {
	name    string
	deny    bool
	reason  string
	preErr  error
	postErr error
	pre     []string // tool names seen by PreTool
	post    []string // tool names seen by PostTool
}

func (h *testHook) Name() string { return h.name }

func (h *testHook) PreTool(_ context.Context, call model.ToolCall, _ toolrouter.Tool) (HookDecision, error) {
	h.pre = append(h.pre, call.Name)
	return HookDecision{Deny: h.deny, Reason: h.reason}, h.preErr
}

func (h *testHook) PostTool(_ context.Context, call model.ToolCall, _ toolrouter.Result) error {
	h.post = append(h.post, call.Name)
	return h.postErr
}

// failPolicy fails the test if it is asked anything.
type failPolicy struct{ t *testing.T }

func (p failPolicy) Authorize(context.Context, policy.Request) (policy.Decision, error) {
	p.t.Error("policy was called")
	return policy.Decision{}, errors.New("unexpected")
}

func hookConfig(hooks ...Hook) Config {
	cfg := baseConfig()
	cfg.Hooks = hooks
	return cfg
}

func TestHookNameValidation(t *testing.T) {
	tests := []struct {
		name  string
		hooks []Hook
		want  string
	}{
		{"space and capital", []Hook{&testHook{name: "Bad Name"}}, `harness: config: invalid hook name "Bad Name"`},
		{"empty", []Hook{&testHook{name: ""}}, `harness: config: invalid hook name ""`},
		{
			"too long",
			[]Hook{&testHook{name: strings.Repeat("a", 41)}},
			`harness: config: invalid hook name "` + strings.Repeat("a", 41) + `"`,
		},
		{
			"duplicate",
			[]Hook{&testHook{name: "a"}, &testHook{name: "b"}, &testHook{name: "a"}},
			`harness: config: duplicate hook name "a"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newLog(t)
			deps := defaultDeps(t)
			deps.Log, deps.Model = store, model.NewFake(textTurn("x"))
			state, err := Run(context.Background(), hookConfig(tt.hooks...), deps)
			if state != nil || err == nil || err.Error() != tt.want {
				t.Fatalf("Run = %v, %v; want error %q", state, err, tt.want)
			}
			if evs, _ := store.Read(context.Background(), "r1", 0); len(evs) != 0 {
				t.Fatalf("%d events written for an invalid config", len(evs))
			}
		})
	}
}

func TestHooksSeeEveryCall(t *testing.T) {
	a, b := &testHook{name: "a"}, &testHook{name: "b"}
	r := runHarness(t, context.Background(), hookConfig(a, b), defaultDeps(t),
		toolTurn(readCall("t1", "x"), readCall("t2", "y")), textTurn("ok"))
	wantStop(t, r.state, stopCompleted, "")
	want := []string{"fs__read_file", "fs__read_file"}
	for _, h := range []*testHook{a, b} {
		if !reflect.DeepEqual(h.pre, want) || !reflect.DeepEqual(h.post, want) {
			t.Fatalf("hook %s: pre %v post %v, want %v", h.name, h.pre, h.post, want)
		}
	}
}

func TestHookDeny(t *testing.T) {
	h := &testHook{name: "pii-guard", deny: true, reason: "no secrets"}
	deps := Deps{Tools: staticTools(t), Policy: failPolicy{t}}
	r := runHarness(t, context.Background(), hookConfig(h), deps, toolTurn(readCall("t1", "x")), textTurn("unused"))
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_completed",
		"tool_call_requested", "policy_decided", "run_completed")
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_DENIED, "rule hook:pii-guard: no secrets")
	pd := r.events[4].GetPolicyDecided()
	if pd.GetRuleId() != "hook:pii-guard" || pd.GetSource() != adlcv1.DecisionSource_DECISION_SOURCE_HOOK ||
		pd.GetDecision() != adlcv1.Decision_DECISION_DENY || pd.GetCallId() != "t1" || pd.GetReason() != "no secrets" {
		t.Fatalf("policy_decided = %v", pd)
	}
	if r.events[4].GetParentSeq() != r.events[3].GetSeq() {
		t.Fatal("policy_decided parent_seq is not the tool_call_requested seq")
	}
	if len(h.post) != 0 {
		t.Fatalf("PostTool ran for a denied call: %v", h.post)
	}
}

func TestHookDenyEmptyReason(t *testing.T) {
	h := &testHook{name: "h", deny: true}
	r := runHarness(t, context.Background(), hookConfig(h), defaultDeps(t), toolTurn(readCall("t1", "x")))
	wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_DENIED, "rule hook:h: denied by hook")
	if got := pick(r.events, (*adlcv1.Event).GetPolicyDecided)[0].GetReason(); got != "denied by hook" {
		t.Fatalf("reason = %q", got)
	}
}

func TestFirstDenyingHookWins(t *testing.T) {
	t.Run("allow then deny", func(t *testing.T) {
		a, b := &testHook{name: "a"}, &testHook{name: "b", deny: true, reason: "nope"}
		r := runHarness(t, context.Background(), hookConfig(a, b), defaultDeps(t), toolTurn(readCall("t1", "x")))
		wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_DENIED, "rule hook:b: nope")
		if len(a.pre) != 1 || len(b.pre) != 1 {
			t.Fatalf("a.pre %v b.pre %v", a.pre, b.pre)
		}
	})
	t.Run("deny then allow", func(t *testing.T) {
		a, b := &testHook{name: "a"}, &testHook{name: "b", deny: true, reason: "nope"}
		r := runHarness(t, context.Background(), hookConfig(b, a), defaultDeps(t), toolTurn(readCall("t1", "x")))
		wantStop(t, r.state, adlcv1.StopReason_STOP_REASON_DENIED, "rule hook:b: nope")
		if len(a.pre) != 0 {
			t.Fatalf("a.pre = %v, want none", a.pre)
		}
	})
}

func TestHookPreError(t *testing.T) {
	h := &testHook{name: "h", preErr: errors.New("boom")}
	r := runHarness(t, context.Background(), hookConfig(h), Deps{Tools: staticTools(t), Policy: failPolicy{t}},
		toolTurn(readCall("t1", "x")))
	wantStop(t, r.state, stopError, "hook h: boom")
	errs := pick(r.events, (*adlcv1.Event).GetErrorOccurred)
	if len(errs) != 1 || errs[0].GetComponent() != "hook:h" || errs[0].GetMessage() != "boom" ||
		errs[0].GetErrorClass() != adlcv1.ErrorClass_ERROR_CLASS_INTERNAL {
		t.Fatalf("error_occurred = %v", errs)
	}
}

func TestHookPostErrorDoesNotStopRun(t *testing.T) {
	h := &testHook{name: "h", postErr: errors.New("late")}
	r := runHarness(t, context.Background(), hookConfig(h), defaultDeps(t), toolTurn(readCall("t1", "x")), textTurn("ok"))
	wantStop(t, r.state, stopCompleted, "")
	errs := pick(r.events, (*adlcv1.Event).GetErrorOccurred)
	if len(errs) != 1 || errs[0].GetComponent() != "hook:h" {
		t.Fatalf("error_occurred = %v", errs)
	}
}

func TestUnknownToolSkipsHooks(t *testing.T) {
	h := &testHook{name: "h"}
	r := runHarness(t, context.Background(), hookConfig(h), defaultDeps(t),
		toolTurn(model.ToolCall{ID: "t1", Name: "nope__x"}), textTurn("ok"))
	wantStop(t, r.state, stopCompleted, "")
	if len(h.pre) != 0 || len(h.post) != 0 {
		t.Fatalf("pre %v post %v, want none", h.pre, h.post)
	}
}
