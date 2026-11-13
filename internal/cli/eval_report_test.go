package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/evals"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
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

// failuresFixture writes a store with one run stopped on its step budget and
// a report holding its unsolved result; it returns the report and db paths.
func failuresFixture(t *testing.T, withRun bool) (reportPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "events.db")
	ctx := context.Background()
	store, err := eventlog.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if withRun {
		for _, p := range []*adlcv1.Event{
			{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{Task: "t"}}},
			{Payload: &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{
				StopReason: adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED, ExhaustedBudget: adlcv1.BudgetKind_BUDGET_KIND_STEPS,
				Detail: "max_steps 3 reached",
			}}},
		} {
			p.RunId, p.AgentId = "run-1", "coder-anthropic@1"
			if _, err := store.Append(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	results := []evals.Result{{
		BugID: "bug-1", Agent: "coder-anthropic@1", Provider: "anthropic", RunID: "run-1",
		StopReason: "STOP_REASON_BUDGET_EXHAUSTED", Grade: &evals.Grade{},
	}}
	reportPath = filepath.Join(dir, "r.json")
	if err := evals.WriteReport(reportPath, evals.Report{Suite: "mini", Results: results, Summary: evals.Summarize(results)}); err != nil {
		t.Fatal(err)
	}
	return reportPath, dbPath
}

func TestEvalReportFailures(t *testing.T) {
	report, db := failuresFixture(t, true)
	code, stdout, stderr := run(t, "eval-report", report, "--failures", "--db", db)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"anthropic  solved 0/1", "budget", "steps: max_steps 3 reached"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q: %q", want, stdout)
		}
	}
}

func TestEvalReportFailuresJSON(t *testing.T) {
	report, db := failuresFixture(t, true)
	code, stdout, stderr := run(t, "eval-report", report, "--failures", "--json", "--db", db)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	var fr evals.FailureReport
	if err := json.Unmarshal([]byte(stdout), &fr); err != nil {
		t.Fatalf("stdout = %q: %v", stdout, err)
	}
	if len(fr.Failures) != 1 || fr.Failures[0].Cause != evals.CauseBudget {
		t.Errorf("failures = %+v", fr.Failures)
	}
}

func TestEvalReportJSONNeedsFailures(t *testing.T) {
	report, _ := failuresFixture(t, true)
	code, _, stderr := run(t, "eval-report", report, "--json")
	if code != 1 || !strings.Contains(stderr, "eval-report: --json needs --failures") {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}

func TestEvalReportFailuresMissingDB(t *testing.T) {
	report, _ := failuresFixture(t, true)
	code, _, stderr := run(t, "eval-report", report, "--failures", "--db", filepath.Join(t.TempDir(), "none.db"))
	if code != 1 || !strings.Contains(stderr, "does not exist") {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}

func TestEvalReportFailuresUnknownRun(t *testing.T) {
	report, db := failuresFixture(t, false)
	code, _, stderr := run(t, "eval-report", report, "--failures", "--db", db)
	if code != 1 || !strings.Contains(stderr, "run not found") {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}
