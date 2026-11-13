package harness

import (
	"fmt"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// exhausted describes a reached limit.
type exhausted struct {
	Kind   adlcv1.BudgetKind
	Detail string
}

// usage is what has been spent so far.
type usage struct {
	Steps     int
	Tokens    int64
	USDMicros int64
	ToolCalls int
	Elapsed   time.Duration
}

// beforeModelCall returns the first limit reached before a model call, or
// nil. The order is wall clock, cost, tokens, steps.
func beforeModelCall(b *adlcv1.Budget, u usage) *exhausted {
	if b == nil {
		return nil
	}
	if e := wallClockExhausted(b, u); e != nil {
		return e
	}
	if m := b.GetMaxUsdMicros(); m > 0 && u.USDMicros >= m {
		return &exhausted{
			adlcv1.BudgetKind_BUDGET_KIND_COST,
			fmt.Sprintf("max_usd_micros %d reached (used %d)", m, u.USDMicros),
		}
	}
	if m := b.GetMaxTokens(); m > 0 && u.Tokens >= m {
		return &exhausted{
			adlcv1.BudgetKind_BUDGET_KIND_TOKENS,
			fmt.Sprintf("max_tokens %d reached (used %d)", m, u.Tokens),
		}
	}
	if m := b.GetMaxSteps(); m > 0 && u.Steps >= int(m) {
		return &exhausted{adlcv1.BudgetKind_BUDGET_KIND_STEPS, fmt.Sprintf("max_steps %d reached", m)}
	}
	return nil
}

// beforeToolCall returns the first limit reached before a tool call, or nil.
// The order is wall clock, tool calls.
func beforeToolCall(b *adlcv1.Budget, u usage) *exhausted {
	if b == nil {
		return nil
	}
	if e := wallClockExhausted(b, u); e != nil {
		return e
	}
	if m := b.GetMaxToolCalls(); m > 0 && u.ToolCalls >= int(m) {
		return &exhausted{adlcv1.BudgetKind_BUDGET_KIND_TOOL_CALLS, fmt.Sprintf("max_tool_calls %d reached", m)}
	}
	return nil
}

func wallClockExhausted(b *adlcv1.Budget, u usage) *exhausted {
	if m := b.GetMaxWallClock().AsDuration(); m > 0 && u.Elapsed >= m {
		return wallClockReached(m)
	}
	return nil
}

func wallClockReached(limit time.Duration) *exhausted {
	return &exhausted{adlcv1.BudgetKind_BUDGET_KIND_WALL_CLOCK, fmt.Sprintf("max_wall_clock %s reached", limit)}
}
