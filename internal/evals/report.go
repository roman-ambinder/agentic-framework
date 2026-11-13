package evals

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

// PrintTable prints one row per result and then one "provider: n/m solved"
// line per provider, sorted by provider name.
func PrintTable(w io.Writer, results []Result) {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "BUG\tPROVIDER\tRESULT\tSTOP\tSECONDS")
	solved := map[string]int{}
	total := map[string]int{}
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", r.BugID, r.Provider, resultWord(r), stopWord(r), seconds(r.DurationMS))
		total[r.Provider]++
		if resultWord(r) == "solved" {
			solved[r.Provider]++
		}
	}
	_ = tw.Flush()
	providers := make([]string, 0, len(total))
	for p := range total {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	for _, p := range providers {
		fmt.Fprintf(w, "%s: %d/%d solved\n", p, solved[p], total[p])
	}
}

// seconds rounds milliseconds to whole seconds.
func seconds(ms int64) int64 { return (ms + 500) / 1000 }
