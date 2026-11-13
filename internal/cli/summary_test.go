package cli

import (
	"bytes"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

func TestFormatUSD(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{{0, "0.000000"}, {1, "0.000001"}, {1234567, "1.234567"}}
	for _, tt := range tests {
		if got := formatUSD(tt.in); got != tt.want {
			t.Errorf("formatUSD(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStopName(t *testing.T) {
	tests := []struct {
		in   adlcv1.StopReason
		want string
	}{
		{adlcv1.StopReason_STOP_REASON_COMPLETED, "completed"},
		{adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED, "budget_exhausted"},
	}
	for _, tt := range tests {
		if got := stopName(tt.in); got != tt.want {
			t.Errorf("stopName(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPrintSummary(t *testing.T) {
	st := &eventlog.RunState{
		RunID: "r1", AgentID: "hello@1",
		StopReason: adlcv1.StopReason_STOP_REASON_ERROR,
		StopDetail: "boom", Result: "done",
		Step: 3, ToolCalls: 2, Errors: 1,
		Usage: eventlog.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 40, USDMicros: 1234567},
	}
	tests := []struct {
		name   string
		mutate func(*eventlog.RunState)
		want   string
	}{
		{"all fields", func(*eventlog.RunState) {}, "run:     r1\n" +
			"agent:   hello@1\n" +
			"stop:    error\n" +
			"detail:  boom\n" +
			"steps:   3  tool calls: 2  errors: 1\n" +
			"tokens:  in 10  out 20  cache read 30  cache write 40\n" +
			"cost:    $1.234567\n" +
			"result:  done\n"},
		{"no result or detail", func(s *eventlog.RunState) { s.Result, s.StopDetail = "", "" }, "run:     r1\n" +
			"agent:   hello@1\n" +
			"stop:    error\n" +
			"steps:   3  tool calls: 2  errors: 1\n" +
			"tokens:  in 10  out 20  cache read 30  cache write 40\n" +
			"cost:    $1.234567\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := *st
			tt.mutate(&s)
			var buf bytes.Buffer
			printSummary(&buf, &s)
			if buf.String() != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", buf.String(), tt.want)
			}
		})
	}
}
