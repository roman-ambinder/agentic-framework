package cli

import (
	"fmt"
	"io"
	"strings"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// printSummary writes the fixed-format run summary to w.
func printSummary(w io.Writer, s *eventlog.RunState) {
	fmt.Fprintf(w, "%-9s%s\n", "run:", s.RunID)
	fmt.Fprintf(w, "%-9s%s\n", "agent:", s.AgentID)
	fmt.Fprintf(w, "%-9s%s\n", "stop:", stopName(s.StopReason))
	if s.StopDetail != "" {
		fmt.Fprintf(w, "%-9s%s\n", "detail:", s.StopDetail)
	}
	fmt.Fprintf(w, "%-9s%d  tool calls: %d  errors: %d\n", "steps:", s.Step, s.ToolCalls, s.Errors)
	fmt.Fprintf(w, "%-9sin %d  out %d  cache read %d  cache write %d\n", "tokens:",
		s.Usage.InputTokens, s.Usage.OutputTokens, s.Usage.CacheReadTokens, s.Usage.CacheWriteTokens)
	fmt.Fprintf(w, "%-9s$%s\n", "cost:", formatUSD(s.Usage.USDMicros))
	if s.Result != "" {
		fmt.Fprintf(w, "%-9s%s\n", "result:", s.Result)
	}
}

// stopName returns the lower-case stop reason without its prefix.
func stopName(r adlcv1.StopReason) string {
	return strings.ToLower(strings.TrimPrefix(r.String(), "STOP_REASON_"))
}

// formatUSD formats micro-USD as dollars with six decimals.
func formatUSD(micros int64) string {
	return fmt.Sprintf("%d.%06d", micros/1_000_000, micros%1_000_000)
}
