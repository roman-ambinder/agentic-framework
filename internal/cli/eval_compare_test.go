package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/evals"
)

// writeCompareReport writes a report with two bugs for agent, one solved.
func writeCompareReport(t *testing.T, agent string) string {
	t.Helper()
	var results []evals.Result
	for i, bug := range []string{"b1", "b2"} {
		results = append(results, evals.Result{
			BugID: bug, Difficulty: evals.Easy, Agent: agent, Provider: "anthropic",
			Grade: &evals.Grade{Solved: i == 0, Steps: 5, USDMicros: 100_000},
		})
	}
	path := filepath.Join(t.TempDir(), agent+".json")
	if err := evals.WriteReport(path, evals.Report{Suite: "mini", Results: results, Summary: evals.Summarize(results)}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEvalCompareText(t *testing.T) {
	code, stdout, stderr := run(t, "eval-compare", writeCompareReport(t, "coder@1"), writeCompareReport(t, "coder@2"))
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "coder@1 -> coder@2  bugs 2") || !strings.Contains(stdout, "status changes:") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestEvalCompareJSON(t *testing.T) {
	code, stdout, stderr := run(t, "eval-compare", "--json", writeCompareReport(t, "coder@1"), writeCompareReport(t, "coder@2"))
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	var c evals.Comparison
	if err := json.Unmarshal([]byte(stdout), &c); err != nil || len(c.Pairs) != 1 || c.Base.Path == "" {
		t.Errorf("err = %v, comparison = %+v", err, c)
	}
}

func TestEvalCompareMapFlag(t *testing.T) {
	a, b := writeCompareReport(t, "a@1"), writeCompareReport(t, "b@1")
	code, _, stderr := run(t, "eval-compare", a, b)
	if code != 1 || !strings.Contains(stderr, "no agent name appears in both reports") {
		t.Errorf("no map: code = %d, stderr = %q", code, stderr)
	}
	if code, _, stderr = run(t, "eval-compare", a, b, "--map", "a@1=b@1"); code != 0 {
		t.Errorf("with map: code = %d, stderr = %q", code, stderr)
	}
	code, _, stderr = run(t, "eval-compare", a, b, "--map", "a@1")
	if code != 1 || !strings.Contains(stderr, "want BASE_AGENT=NEW_AGENT") {
		t.Errorf("bad map: code = %d, stderr = %q", code, stderr)
	}
}

func TestEvalCompareMissingFile(t *testing.T) {
	code, _, stderr := run(t, "eval-compare", filepath.Join(t.TempDir(), "nope.json"), writeCompareReport(t, "a@1"))
	if code != 1 || !strings.Contains(stderr, "read report") {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}
