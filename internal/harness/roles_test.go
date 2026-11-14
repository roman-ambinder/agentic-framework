package harness

import (
	"bytes"
	"context"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/costreport"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/observability"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
)

// namedAdapter is a Fake that reports another provider name, so two fakes can be told apart.
type namedAdapter struct {
	*model.Fake
	provider string
}

func (n namedAdapter) Provider() string { return n.provider }

func (n namedAdapter) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	resp, err := n.Fake.Generate(ctx, req)
	if resp != nil {
		resp.Provider = n.provider
	}
	return resp, err
}

// rolesRun is the common setup: an act fake and a planner fake.
type rolesRun struct {
	act     *model.Fake
	planner namedAdapter
}

func roleConfig() Config {
	cfg := baseConfig()
	cfg.Effort = "high"
	cfg.Plan = PlanConfig{Enabled: true, Model: "plan-m", Effort: "low"}
	return cfg
}

func usageTurn(text string) model.FakeTurn {
	t := textTurn(text)
	t.Response.Usage = model.Usage{InputTokens: 10}
	return t
}

func runRoles(t *testing.T, cfg Config, actTurn, planTurn model.FakeTurn) (rolesRun, []*adlcv1.Event) {
	t.Helper()
	store := newLog(t)
	r := rolesRun{act: model.NewFake(actTurn), planner: namedAdapter{model.NewFake(planTurn), "planner"}}
	deps := Deps{Log: store, Model: r.act, PlanModel: r.planner, Tools: staticTools(t), Policy: policy.AllowAll{}}
	state, err := Run(context.Background(), cfg, deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	events := assertFolds(t, store, cfg.RunID, state)
	return r, events
}

func TestPlanRoleUsesPlanAdapter(t *testing.T) {
	r, _ := runRoles(t, roleConfig(), textTurn("ok"), textTurn("the plan"))
	preqs := r.planner.Requests()
	if len(preqs) != 1 {
		t.Fatalf("planner got %d requests, want 1", len(preqs))
	}
	if p := preqs[0]; p.Model != "plan-m" || p.Effort != "low" || len(p.Tools) != 0 || p.MaxTokens != 1024 {
		t.Errorf("plan request = model %q effort %q tools %d max %d", p.Model, p.Effort, len(p.Tools), p.MaxTokens)
	}
	areqs := r.act.Requests()
	if len(areqs) != 1 {
		t.Fatalf("act got %d requests, want 1", len(areqs))
	}
	a := areqs[0]
	if a.Model != "m" || a.Effort != "high" || len(a.Tools) != 1 || a.Messages[1].Text() != "the plan" {
		t.Errorf("act request = model %q effort %q tools %d msg1 %q", a.Model, a.Effort, len(a.Tools), a.Messages[1].Text())
	}
	if r.act.Remaining() != 0 || r.planner.Remaining() != 0 {
		t.Errorf("remaining turns: act %d, planner %d", r.act.Remaining(), r.planner.Remaining())
	}
}

func TestPlanRoleRecordsProviderAndModel(t *testing.T) {
	_, events := runRoles(t, roleConfig(), textTurn("ok"), textTurn("the plan"))
	type pm struct{ call, provider, model string }
	started := func(i int) pm {
		s := events[i-1].GetModelCallStarted()
		return pm{s.GetCallId(), s.GetProvider(), s.GetModel()}
	}
	completed := func(i int) pm {
		c := events[i-1].GetModelCallCompleted()
		return pm{"", c.GetProvider(), c.GetModel()}
	}
	if got, want := started(2), (pm{"plan", "planner", "plan-m"}); got != want {
		t.Errorf("event 2 = %+v, want %+v", got, want)
	}
	if got, want := completed(3), (pm{"", "planner", "plan-m"}); got != want {
		t.Errorf("event 3 = %+v, want %+v", got, want)
	}
	if got, want := started(4), (pm{"m1", "fake", "m"}); got != want {
		t.Errorf("event 4 = %+v, want %+v", got, want)
	}
	if got, want := completed(5), (pm{"", "fake", "m"}); got != want {
		t.Errorf("event 5 = %+v, want %+v", got, want)
	}
	if s := events[1].GetModelCallStarted(); s.GetStep() != 0 {
		t.Errorf("plan step = %d, want 0", s.GetStep())
	}
}

func pricedConfig() Config {
	cfg := roleConfig()
	cfg.Price = model.Price{InputPerMTok: 1}
	cfg.Prices = map[string]model.Price{
		"m":      {InputPerMTok: 1_000_000},
		"plan-m": {InputPerMTok: 3_000_000},
	}
	return cfg
}

func TestPlanRolePricedByPlanModel(t *testing.T) {
	store := newLog(t)
	deps := Deps{
		Log: store, Model: model.NewFake(usageTurn("ok")), Tools: staticTools(t), Policy: policy.AllowAll{},
		PlanModel: namedAdapter{model.NewFake(usageTurn("the plan")), "planner"},
	}
	state, err := Run(context.Background(), pricedConfig(), deps)
	if err != nil {
		t.Fatal(err)
	}
	events := assertFolds(t, store, "r1", state)
	if got := events[2].GetCost().GetUsdMicros(); got != 30 {
		t.Errorf("plan usd_micros = %d, want 30", got)
	}
	if got := events[4].GetCost().GetUsdMicros(); got != 10 {
		t.Errorf("m1 usd_micros = %d, want 10", got)
	}
	if state.Usage.USDMicros != 40 {
		t.Errorf("state usd = %d, want 40", state.Usage.USDMicros)
	}
	rep, err := costreport.Build(costreport.ByModel, map[string][]*adlcv1.Event{"r1": events})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rep.Rows)
	}
	for i, want := range []struct {
		key  string
		usd  int64
		call int
	}{{"fake/m", 10, 1}, {"planner/plan-m", 30, 1}} {
		if r := rep.Rows[i]; r.Key != want.key || r.USDMicros != want.usd || r.ModelCalls != want.call {
			t.Errorf("row %d = %+v, want %+v", i, r, want)
		}
	}
}

func TestPlanRoleTrace(t *testing.T) {
	_, events := runRoles(t, roleConfig(), textTurn("ok"), textTurn("the plan"))
	root, err := observability.Build(events)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := observability.WriteTree(&buf, root); err != nil {
		t.Fatal(err)
	}
	var planLine, actLine bool
	for _, line := range strings.Split(buf.String(), "\n") {
		planLine = planLine || (strings.Contains(line, "model plan") && strings.Contains(line, "planner/plan-m"))
		actLine = actLine || (strings.Contains(line, "model m1") && strings.Contains(line, "fake/m"))
	}
	if !planLine || !actLine {
		t.Errorf("tree lacks the plan or act model line (plan %v, act %v):\n%s", planLine, actLine, buf.String())
	}
}

func TestPlanModelWithoutPlanAdapter(t *testing.T) {
	r := runHarness(t, context.Background(), roleConfig(), defaultDeps(t), textTurn("the plan"), textTurn("ok"))
	reqs := r.model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if reqs[0].Model != "plan-m" || reqs[0].Effort != "low" || reqs[1].Model != "m" || reqs[1].Effort != "high" {
		t.Errorf("requests = (%q, %q), (%q, %q)", reqs[0].Model, reqs[0].Effort, reqs[1].Model, reqs[1].Effort)
	}
	if s := r.events[1].GetModelCallStarted(); s.GetProvider() != "fake" || s.GetModel() != "plan-m" {
		t.Errorf("event 2 = %v", s)
	}
}

func TestPlanAdapterWithoutPlanModelRejected(t *testing.T) {
	store := newLog(t)
	cfg := baseConfig()
	cfg.Plan = PlanConfig{Enabled: true}
	deps := Deps{
		Log: store, Model: model.NewFake(), Tools: staticTools(t), Policy: policy.AllowAll{},
		PlanModel: model.NewFake(),
	}
	_, err := Run(context.Background(), cfg, deps)
	if err == nil || err.Error() != "harness: config: plan model is empty but deps.PlanModel is set" {
		t.Fatalf("err = %v", err)
	}
	if evs, _ := store.Read(context.Background(), "r1", 0); len(evs) != 0 {
		t.Errorf("log has %d events, want 0", len(evs))
	}
}

func TestPlanRoleIgnoredWhenPlanningDisabled(t *testing.T) {
	cfg := roleConfig()
	cfg.Plan.Enabled = false
	r, events := runRoles(t, cfg, textTurn("ok"), textTurn("the plan"))
	if n := len(r.planner.Requests()); n != 0 {
		t.Errorf("planner got %d requests, want 0", n)
	}
	wantKinds(t, events, "run_started", "model_call_started", "model_call_completed", "run_completed")
}

func TestResumeUsesPlanAdapter(t *testing.T) {
	cfg := roleConfig()
	deps := resumeDeps(t)
	deps.PlanModel = namedAdapter{model.NewFake(textTurn("the plan")), "planner"}
	store := crashRun(t, 2, cfg, deps, textTurn("ok"))

	planner := namedAdapter{model.NewFake(textTurn("the plan")), "planner"}
	deps = resumeDeps(t)
	deps.PlanModel = planner
	r := resumeOK(t, store, cfg, deps, textTurn("ok"))
	reqs := planner.Requests()
	if len(reqs) != 1 || reqs[0].Model != "plan-m" {
		t.Fatalf("planner requests = %+v", reqs)
	}
	var provider string
	for _, ev := range r.tail {
		if c := ev.GetModelCallCompleted(); c.GetCallId() == PlanCallID {
			provider = c.GetProvider()
		}
	}
	if provider != "planner" {
		t.Errorf("plan completed provider = %q, want planner", provider)
	}
	wantStop(t, r.state, stopCompleted, "")
}
