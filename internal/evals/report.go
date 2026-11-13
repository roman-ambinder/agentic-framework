package evals

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Report is the JSON document `adlc eval` writes.
type Report struct {
	Suite      string    `json:"suite"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Results    []Result  `json:"results"`
	Summary    Summary   `json:"summary"`
}

// WriteReport writes rep to path as JSON with 2-space indent and a trailing
// newline, creating the parent directory.
func WriteReport(path string, rep Report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("evals: encode report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("evals: write report: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("evals: write report: %w", err)
	}
	return nil
}

// resultWord is "error", "solved" or "failed".
func resultWord(r Result) string {
	switch {
	case r.Error != "":
		return "error"
	case r.Solved():
		return "solved"
	default:
		return "failed"
	}
}

// stopWord is the stop reason without its STOP_REASON_ prefix, or "-".
func stopWord(r Result) string {
	s := strings.TrimPrefix(r.StopReason, "STOP_REASON_")
	if s == "" {
		return "-"
	}
	return s
}

// PrintTable prints one row per result, a blank line and one summary line per
// provider (sorted by name), and a note when some results had errors.
func PrintTable(w io.Writer, rep Report) {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "BUG\tPROVIDER\tRESULT\tSTOP\tSTEPS\tTOOLS\tWASTED\tCOST\tSECONDS")
	for _, r := range rep.Results {
		steps, tools, wasted, cost := "-", "-", "-", "-"
		if g := r.Grade; r.Error == "" && g != nil {
			steps, tools, wasted, cost = strconv.Itoa(g.Steps), strconv.Itoa(g.ToolCalls), strconv.Itoa(g.WastedCalls), usd(g.USDMicros)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			r.BugID, r.Provider, resultWord(r), stopWord(r), steps, tools, wasted, cost, seconds(r.DurationMS))
	}
	_ = tw.Flush()
	printSummary(w, rep.Summary)
}

// printSummary prints the per-provider lines and the errors note.
func printSummary(w io.Writer, s Summary) {
	providers := make([]string, 0, len(s.ByProvider))
	width, anyErrors := 0, false
	for p, ps := range s.ByProvider {
		providers = append(providers, p)
		width = max(width, len(p))
		anyErrors = anyErrors || ps.Errors > 0
	}
	sort.Strings(providers)
	if len(providers) > 0 {
		fmt.Fprintln(w)
	}
	for _, p := range providers {
		ps := s.ByProvider[p]
		fmt.Fprintf(w, "%-*s  solved %d/%d (pass@1 %.3f)  easy %s  medium %s  hard %s  median %s  median steps %s  total %s  errors %d\n",
			width, p, ps.Solved, ps.Runs, ps.PassAt1,
			frac(ps.ByDifficulty[Easy]), frac(ps.ByDifficulty[Medium]), frac(ps.ByDifficulty[Hard]),
			usd(ps.MedianUSDMicrosSolved), strconv.FormatFloat(ps.MedianStepsSolved, 'f', -1, 64),
			usd(ps.TotalUSDMicros), ps.Errors)
	}
	if anyErrors {
		fmt.Fprintln(w, "note: results with errors are excluded from pass@1")
	}
}

func frac(b Bucket) string { return fmt.Sprintf("%d/%d", b.Solved, b.Runs) }

func usd(micros int64) string { return fmt.Sprintf("$%.2f", float64(micros)/1e6) }

// seconds rounds milliseconds to whole seconds.
func seconds(ms int64) int64 { return (ms + 500) / 1000 }
