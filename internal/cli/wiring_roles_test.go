package cli

import (
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/model/anthropic"
)

func roleAgent(role string) string {
	y := "name: x\nversion: 1\nmodel:\n  provider: anthropic\n  name: m1\n"
	if role != "" {
		y += "  roles:\n    plan:\n" + role
	}
	return y + "policy:\n  default: deny\n"
}

func TestBuildPlanModel(t *testing.T) {
	t.Run("no role is a plain nil", func(t *testing.T) {
		m, err := buildPlanModel(parseAgent(t, roleAgent("")), "")
		if m != nil || err != nil {
			t.Fatalf("m = %v, err = %v, want nil, nil", m, err)
		}
	})
	t.Run("anthropic role", func(t *testing.T) {
		m, err := buildPlanModel(parseAgent(t, roleAgent("      provider: anthropic\n      name: p1\n")), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := m.(*anthropic.Adapter); !ok {
			t.Fatalf("m = %T, want *anthropic.Adapter", m)
		}
	})
	t.Run("fake role needs a script", func(t *testing.T) {
		_, err := buildPlanModel(parseAgent(t, roleAgent("      provider: fake\n      name: p1\n")), "")
		if err == nil || err.Error() != "run: provider fake needs --fake-script" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestHarnessConfigPassesPlanRole(t *testing.T) {
	role := "      provider: openai\n      name: plan-1\n      effort: low\n      pricing: {input_per_mtok: \"2\"}\n"
	cfg := harnessConfig(parseAgent(t, roleAgent(role)), "r1", "task")
	if cfg.Plan.Model != "plan-1" || cfg.Plan.Effort != "low" {
		t.Errorf("Plan = %+v", cfg.Plan)
	}
	if got := cfg.Prices["plan-1"].InputPerMTok; got != 2_000_000 {
		t.Errorf("Prices[plan-1].InputPerMTok = %d, want 2000000", got)
	}
	cfg = harnessConfig(parseAgent(t, roleAgent("")), "r1", "task")
	if cfg.Plan.Model != "" || cfg.Plan.Effort != "" {
		t.Errorf("Plan = %+v, want empty model and effort", cfg.Plan)
	}
}

func TestBuildRunPassesPlanModel(t *testing.T) {
	a := parseAgent(t, roleAgent("      provider: openai\n      name: plan-1\n"))
	t.Setenv("OPENAI_API_KEY", "x")
	_, deps, cleanup, err := buildRun(t.Context(), a, runOptions{task: "t"}, "r1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	if deps.PlanModel == nil || deps.PlanModel.Provider() != "openai" || deps.Model.Provider() != "anthropic" {
		t.Errorf("PlanModel = %v, Model = %v", deps.PlanModel, deps.Model)
	}
}
