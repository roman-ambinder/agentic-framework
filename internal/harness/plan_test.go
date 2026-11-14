package harness

import (
	"context"
	"reflect"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

func planConfig() Config {
	cfg := baseConfig()
	cfg.Plan = PlanConfig{Enabled: true}
	return cfg
}

func planTurn() model.FakeTurn { return textTurn("1. read a.go") }

func planStarts(evs []*adlcv1.Event) []*adlcv1.ModelCallStarted {
	return pick(evs, (*adlcv1.Event).GetModelCallStarted)
}

func TestPlanDisabledChangesNothing(t *testing.T) {
	r := runHarness(t, context.Background(), baseConfig(), defaultDeps(t),
		toolTurn(readCall("t1", "a.go")), textTurn("ok"))
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_completed",
		"tool_call_requested", "policy_decided", "tool_call_completed", "checkpoint",
		"model_call_started", "model_call_completed", "run_completed")
	reqs := r.model.Requests()
	if len(reqs) != 2 || len(reqs[0].Messages) != 1 || len(reqs[0].Tools) != 1 {
		t.Fatalf("requests = %d, first has %d messages and %d tools", len(reqs), len(reqs[0].Messages), len(reqs[0].Tools))
	}
	for _, ev := range r.events {
		if ev.GetModelCallStarted().GetCallId() == PlanCallID || ev.GetModelCallCompleted().GetCallId() == PlanCallID {
			t.Fatal("a plan event was written with planning off")
		}
	}
}

func TestPlanEnabledRecordsPlanCall(t *testing.T) {
	r := runHarness(t, context.Background(), planConfig(), defaultDeps(t),
		planTurn(), toolTurn(readCall("t1", "a.go")), textTurn("ok"))
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_completed",
		"model_call_started", "model_call_completed", "tool_call_requested", "policy_decided",
		"tool_call_completed", "checkpoint", "model_call_started", "model_call_completed", "run_completed")
	s := r.events[1].GetModelCallStarted()
	if s.GetCallId() != PlanCallID || s.GetStep() != 0 || s.GetAttempt() != 1 {
		t.Fatalf("event 2 = %v", s)
	}
	c := r.events[2].GetModelCallCompleted()
	if c.GetCallId() != PlanCallID || c.GetText() != "1. read a.go" || len(c.GetToolCalls()) != 0 {
		t.Fatalf("event 3 = %v", c)
	}
	if s := r.events[3].GetModelCallStarted(); s.GetCallId() != "m1" || s.GetStep() != 1 {
		t.Fatalf("event 4 = %v", s)
	}
	if cp := r.events[8].GetCheckpoint(); cp.GetStep() != 1 {
		t.Fatalf("checkpoint = %v", cp)
	}
	if r.state.Step != 2 {
		t.Fatalf("state.Step = %d, want 2", r.state.Step)
	}
	wantStop(t, r.state, stopCompleted, "")
	if r.state.Result != "ok" {
		t.Fatalf("result = %q", r.state.Result)
	}
}

func TestPlanRequest(t *testing.T) {
	tests := []struct {
		name       string
		plan       PlanConfig
		wantPrompt string
		wantMax    int
	}{
		{"defaults", PlanConfig{Enabled: true}, DefaultPlanPrompt, DefaultPlanMaxTokens},
		{"custom", PlanConfig{Enabled: true, Prompt: "plan please", MaxTokens: 50}, "plan please", 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.System = "sys"
			cfg.Plan = tt.plan
			r := runHarness(t, context.Background(), cfg, defaultDeps(t), planTurn(), textTurn("ok"))
			req := r.model.Requests()[0]
			if len(req.Tools) != 0 || req.MaxTokens != tt.wantMax || req.System != "sys" || len(req.Messages) != 1 {
				t.Fatalf("plan request = %+v", req)
			}
			var texts []string
			for _, p := range req.Messages[0].Parts {
				texts = append(texts, p.Text)
			}
			if want := []string{"do it", tt.wantPrompt}; !reflect.DeepEqual(texts, want) {
				t.Fatalf("parts = %q, want %q", texts, want)
			}
		})
	}
}

func TestPlanInEveryLaterRequest(t *testing.T) {
	turns := append([]model.FakeTurn{planTurn()}, toolTurns(2, model.Usage{})...)
	r := runHarness(t, context.Background(), planConfig(), defaultDeps(t), append(turns, textTurn("ok"))...)
	reqs := r.model.Requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want 4", len(reqs))
	}
	for i := 1; i < len(reqs); i++ {
		m := reqs[i].Messages
		if m[1].Role != model.RoleAssistant || m[1].Text() != "1. read a.go" ||
			m[2].Role != model.RoleUser || m[2].Text() != PlanFollowUp || len(reqs[i].Tools) != 1 {
			t.Fatalf("request %d messages = %q, tools %d", i, msgTexts(m), len(reqs[i].Tools))
		}
		if i >= 2 {
			checkPrefix(t, reqs[i-1], reqs[i])
		}
	}
}

func TestPlanNotCountedAsStep(t *testing.T) {
	cfg := planConfig()
	cfg.Budget = &adlcv1.Budget{MaxSteps: 2}
	r := runHarness(t, context.Background(), cfg, defaultDeps(t),
		append([]model.FakeTurn{planTurn()}, toolTurns(5, model.Usage{})...)...)
	if n := countModelCalls(r.events); n != 3 {
		t.Fatalf("%d model calls, want 3", n)
	}
	wantBudgetStop(t, r.state, budgetSteps, "max_steps 2 reached")
}

func planUsageTurn(u model.Usage) model.FakeTurn {
	t := planTurn()
	t.Response.Usage = u
	return t
}

func TestPlanCountsTowardTokens(t *testing.T) {
	cfg := planConfig()
	cfg.Budget = &adlcv1.Budget{MaxTokens: 100}
	u := model.Usage{InputTokens: 40, OutputTokens: 20}
	r := runHarness(t, context.Background(), cfg, defaultDeps(t),
		append([]model.FakeTurn{planUsageTurn(u)}, toolTurns(5, u)...)...)
	if n := countModelCalls(r.events); n != 2 {
		t.Fatalf("%d model calls, want 2", n)
	}
	wantBudgetStop(t, r.state, budgetTokens, "max_tokens 100 reached (used 120)")
}

func TestPlanCountsTowardCost(t *testing.T) {
	cfg := planConfig()
	cfg.Price = model.Price{InputPerMTok: 6_000_000}
	cfg.Budget = &adlcv1.Budget{MaxUsdMicros: 10}
	u := model.Usage{InputTokens: 1}
	r := runHarness(t, context.Background(), cfg, defaultDeps(t),
		append([]model.FakeTurn{planUsageTurn(u)}, toolTurns(5, u)...)...)
	if n := countModelCalls(r.events); n != 2 {
		t.Fatalf("%d model calls, want 2", n)
	}
	wantBudgetStop(t, r.state, budgetCost, "max_usd_micros 10 reached (used 12)")
	if got := r.events[2].GetCost().GetUsdMicros(); got != 6 {
		t.Fatalf("plan cost = %d, want 6", got)
	}
}

func TestPlanToolCallsDropped(t *testing.T) {
	r := runHarness(t, context.Background(), planConfig(), defaultDeps(t),
		toolTurn(readCall("t1", "a")), textTurn("ok"))
	if n := len(pick(r.events, (*adlcv1.Event).GetToolCallRequested)); n != 0 {
		t.Fatalf("%d tool_call_requested, want 0", n)
	}
	if c := r.events[2].GetModelCallCompleted(); c.GetCallId() != PlanCallID || len(c.GetToolCalls()) != 0 {
		t.Fatalf("plan completed = %v", c)
	}
	if n := len(r.model.Requests()[1].Messages); n != 1 {
		t.Fatalf("request 1 has %d messages, want 1", n)
	}
}

func TestPlanRefusalStopsRun(t *testing.T) {
	r := runHarness(t, context.Background(), planConfig(), defaultDeps(t),
		respWithFinish(planTurn(), adlcv1.FinishReason_FINISH_REASON_REFUSAL), textTurn("unused"))
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_completed", "run_completed")
	wantStop(t, r.state, stopError, "model refused")
	if n := len(r.model.Requests()); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}

func TestPlanRetried(t *testing.T) {
	deps, _ := retryDeps(t)
	r := runHarness(t, context.Background(), planConfig(), deps, rateLimited(), planTurn(), textTurn("ok"))
	if f := pick(r.events, (*adlcv1.Event).GetModelCallFailed); len(f) != 1 || f[0].GetCallId() != PlanCallID {
		t.Fatalf("failures = %v", f)
	}
	s := planStarts(r.events)
	if len(s) < 2 || s[1].GetCallId() != PlanCallID || s[1].GetStep() != 0 || s[1].GetAttempt() != 2 {
		t.Fatalf("starts = %v", s)
	}
	wantStop(t, r.state, stopCompleted, "")
}

// The kill check runs before the plan call, as before every model call.
func TestPlanKilledBeforePlanCall(t *testing.T) {
	fake := &fakeKillSwitch{killAt: 1, reason: "stop now"}
	r := runHarness(t, context.Background(), planConfig(), killDeps(t, fake), planTurn(), textTurn("ok"))
	wantKinds(t, r.events, "run_started", "run_completed")
	wantStop(t, r.state, stopCancelled, "killed: stop now")
	if r.model.Remaining() != 2 {
		t.Fatalf("Remaining = %d, want 2", r.model.Remaining())
	}
}

// A retry of the plan call is checked too (callModel checks before attempt 2).
func TestPlanKilledBeforePlanRetry(t *testing.T) {
	fake := &fakeKillSwitch{killAt: 2, reason: "why"}
	deps := killDeps(t, fake)
	deps.Sleep = (&sleepRecorder{}).sleep
	r := runHarness(t, context.Background(), planConfig(), deps, rateLimited(), planTurn(), textTurn("ok"))
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_failed", "run_completed")
	wantStop(t, r.state, stopCancelled, "killed: why")
}

// A kill that arrives after the plan stops the run before step 1.
func TestPlanKilledAfterPlan(t *testing.T) {
	fake := &fakeKillSwitch{killAt: 2, reason: "why"}
	r := runHarness(t, context.Background(), planConfig(), killDeps(t, fake), planTurn(), textTurn("ok"))
	wantKinds(t, r.events, "run_started", "model_call_started", "model_call_completed", "run_completed")
	wantStop(t, r.state, stopCancelled, "killed: why")
}

func TestPlanCancelledBeforePlanCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := runHarness(t, ctx, planConfig(), defaultDeps(t), planTurn(), textTurn("ok"))
	wantKinds(t, r.events, "run_started", "run_completed")
	wantStop(t, r.state, stopCancelled, "")
}

func TestPlanInvalidConfig(t *testing.T) {
	cfg := planConfig()
	cfg.Plan.MaxTokens = -1
	deps := defaultDeps(t)
	deps.Log, deps.Model = newLog(t), model.NewFake()
	if _, err := Run(context.Background(), cfg, deps); err == nil {
		t.Fatal("want an error for a negative plan max tokens")
	}
}

func TestLastModelCompletedSkipsPlan(t *testing.T) {
	plan := evModel(&adlcv1.ModelCallCompleted{CallId: PlanCallID, Text: "P"})
	step := evModel(&adlcv1.ModelCallCompleted{CallId: "m1", Text: "S"})
	if mc, tail := lastModelCompleted([]*adlcv1.Event{evStarted(), plan}); mc != nil || len(tail) != 2 {
		t.Fatalf("plan only: mc = %v, tail %d", mc, len(tail))
	}
	if mc, tail := lastModelCompleted([]*adlcv1.Event{evStarted(), plan, step, plan}); mc.GetCallId() != "m1" || len(tail) != 1 {
		t.Fatalf("mc = %v, tail %d", mc, len(tail))
	}
}

// Approve continues a paused run that has a plan: the next step number does
// not count the plan, and the plan stays in the request.
func TestApproveRunWithPlan(t *testing.T) {
	cfg := planConfig()
	e := pausedRun(t, cfg, nil, nil, nil, nil, planTurn(), toolTurn(writeCall("w1")))
	r := e.approve(t, yes("a-w1"), textTurn("done"))
	wantKinds(t, r.tail[:4], "approval_resolved", "tool_call_completed", "checkpoint", "model_call_started")
	if s := r.tail[3].GetModelCallStarted(); s.GetCallId() != "m2" || s.GetStep() != 2 {
		t.Fatalf("started = %v, want m2", s)
	}
	wantStop(t, r.state, stopCompleted, "")
	if r.model.Remaining() != 0 || len(r.model.Requests()) != 1 {
		t.Fatalf("requests = %d", len(r.model.Requests()))
	}
	m := r.model.Requests()[0].Messages
	if m[1].Text() != "1. read a.go" || m[2].Text() != PlanFollowUp || len(m) != 5 {
		t.Fatalf("messages = %q", msgTexts(m))
	}
}
