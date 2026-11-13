package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/evals"
)

func TestEvalReportPrintsTable(t *testing.T) {
	results := []evals.Result{{
		BugID: "ok-001", Provider: "anthropic", Difficulty: evals.Easy, StopReason: "STOP_REASON_COMPLETED",
		Grade: &evals.Grade{Solved: true, Steps: 5, USDMicros: 250_000},
	}}
	path := filepath.Join(t.TempDir(), "r.json")
	if err := evals.WriteReport(path, evals.Report{Suite: "mini", Results: results, Summary: evals.Summarize(results)}); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "eval-report", path)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "anthropic  solved 1/1 (pass@1 1.000)  easy 1/1") || !strings.Contains(stdout, "ok-001") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestEvalReportMissingFile(t *testing.T) {
	code, _, stderr := run(t, "eval-report", filepath.Join(t.TempDir(), "nope.json"))
	if code != 1 || !strings.Contains(stderr, "read report") {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}

func TestEvalReportNotAReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := run(t, "eval-report", path)
	if code != 1 || !strings.Contains(stderr, "read report") {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}
