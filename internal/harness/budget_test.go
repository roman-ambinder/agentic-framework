package harness

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

func TestBudgetNilMeansNoLimit(t *testing.T) {
	u := usage{Steps: 1 << 30, Tokens: 1 << 60, USDMicros: 1 << 60, ToolCalls: 1 << 30, Elapsed: 1000 * time.Hour}
	if e := beforeModelCall(nil, u); e != nil {
		t.Fatalf("beforeModelCall = %+v", e)
	}
	if e := beforeToolCall(nil, u); e != nil {
		t.Fatalf("beforeToolCall = %+v", e)
	}
}

func TestBudgetZeroFieldMeansNoLimit(t *testing.T) {
	u := usage{Steps: 1 << 30, Tokens: 1 << 60, USDMicros: 1 << 60, ToolCalls: 1 << 30, Elapsed: 1000 * time.Hour}
	b := &adlcv1.Budget{}
	if e := beforeModelCall(b, u); e != nil {
		t.Fatalf("beforeModelCall = %+v", e)
	}
	if e := beforeToolCall(b, u); e != nil {
		t.Fatalf("beforeToolCall = %+v", e)
	}
}

func TestBudgetEachLimit(t *testing.T) {
	tests := []struct {
		name   string
		budget *adlcv1.Budget
		at     usage
		below  usage
		tool   bool
		kind   adlcv1.BudgetKind
		detail string
	}{
		{
			"steps", &adlcv1.Budget{MaxSteps: 4},
			usage{Steps: 4},
			usage{Steps: 3},
			false,
			adlcv1.BudgetKind_BUDGET_KIND_STEPS, "max_steps 4 reached",
		},
		{
			"tokens", &adlcv1.Budget{MaxTokens: 100},
			usage{Tokens: 100},
			usage{Tokens: 99},
			false,
			adlcv1.BudgetKind_BUDGET_KIND_TOKENS, "max_tokens 100 reached (used 100)",
		},
		{
			"wall clock", &adlcv1.Budget{MaxWallClock: durationpb.New(10 * time.Minute)},
			usage{Elapsed: 10 * time.Minute},
			usage{Elapsed: 10*time.Minute - 1},
			false,
			adlcv1.BudgetKind_BUDGET_KIND_WALL_CLOCK, "max_wall_clock 10m0s reached",
		},
		{
			"cost", &adlcv1.Budget{MaxUsdMicros: 50},
			usage{USDMicros: 50},
			usage{USDMicros: 49},
			false,
			adlcv1.BudgetKind_BUDGET_KIND_COST, "max_usd_micros 50 reached (used 50)",
		},
		{
			"tool calls", &adlcv1.Budget{MaxToolCalls: 3},
			usage{ToolCalls: 3},
			usage{ToolCalls: 2},
			true,
			adlcv1.BudgetKind_BUDGET_KIND_TOOL_CALLS, "max_tool_calls 3 reached",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := beforeModelCall
			if tt.tool {
				check = beforeToolCall
			}
			e := check(tt.budget, tt.at)
			if e == nil || e.Kind != tt.kind || e.Detail != tt.detail {
				t.Fatalf("at limit: %+v, want %s %q", e, tt.kind, tt.detail)
			}
			if e := check(tt.budget, tt.below); e != nil {
				t.Fatalf("below limit: %+v", e)
			}
		})
	}
}

func TestBudgetOrder(t *testing.T) {
	all := &adlcv1.Budget{MaxSteps: 1, MaxTokens: 1, MaxUsdMicros: 1, MaxWallClock: durationpb.New(time.Second)}
	u := usage{Steps: 1, Tokens: 1, USDMicros: 1, Elapsed: time.Second}
	tests := []struct {
		name string
		b    *adlcv1.Budget
		want adlcv1.BudgetKind
	}{
		{"all set", all, adlcv1.BudgetKind_BUDGET_KIND_WALL_CLOCK},
		{"no wall clock", &adlcv1.Budget{MaxSteps: 1, MaxTokens: 1, MaxUsdMicros: 1}, adlcv1.BudgetKind_BUDGET_KIND_COST},
		{"no wall clock, cost", &adlcv1.Budget{MaxSteps: 1, MaxTokens: 1}, adlcv1.BudgetKind_BUDGET_KIND_TOKENS},
		{"steps only", &adlcv1.Budget{MaxSteps: 1}, adlcv1.BudgetKind_BUDGET_KIND_STEPS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if e := beforeModelCall(tt.b, u); e == nil || e.Kind != tt.want {
				t.Fatalf("got %+v, want %s", e, tt.want)
			}
		})
	}
}

func TestBudgetToolCallOrder(t *testing.T) {
	b := &adlcv1.Budget{MaxToolCalls: 1, MaxWallClock: durationpb.New(time.Second)}
	e := beforeToolCall(b, usage{ToolCalls: 1, Elapsed: time.Second})
	if e == nil || e.Kind != adlcv1.BudgetKind_BUDGET_KIND_WALL_CLOCK {
		t.Fatalf("got %+v", e)
	}
}
