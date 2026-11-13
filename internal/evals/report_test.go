package evals

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleResults() []Result {
	return []Result{
		{
			BugID: "zz-first-row-openai", Provider: "openai", StopReason: "STOP_REASON_COMPLETED", DurationMS: 1000,
			Hidden: TestOutcome{Passed: []string{"TestHiddenZ"}},
		},
		{
			BugID: "stock-negative-reserve", Provider: "anthropic", StopReason: "STOP_REASON_COMPLETED", DurationMS: 84_000,
			Hidden: TestOutcome{Passed: []string{"TestHiddenA"}}, ChangedFiles: []string{"a.go"},
		},
		{
			BugID: "fx-rounding", Provider: "openai", StopReason: "STOP_REASON_BUDGET_EXHAUSTED", DurationMS: 120_400,
			Hidden: TestOutcome{Failed: []string{"TestHiddenB"}, ExitCode: 1},
		},
		{BugID: "journal-order", Provider: "anthropic", Error: "create sandbox: boom", DurationMS: 100},
	}
}

func TestWriteReportRoundTrip(t *testing.T) {
	rep := Report{
		Suite:     "go-bugs",
		StartedAt: time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC), FinishedAt: time.Date(2026, 11, 13, 1, 5, 3, 0, time.UTC),
		Results: sampleResults(),
	}
	path := filepath.Join(t.TempDir(), "sub", "r.json")
	if err := WriteReport(path, rep); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("}\n")) || !bytes.Contains(data, []byte("\n  \"suite\"")) {
		t.Errorf("not 2-space indented with trailing newline:\n%s", data)
	}
	var got Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, rep) {
		t.Errorf("round trip differs:\n got %+v\nwant %+v", got, rep)
	}
}

func gradedResult(bug, provider string, d Difficulty, solved bool, stop string, steps, tools, wasted int, usd int64, ms int64) Result {
	return Result{
		BugID: bug, Provider: provider, Difficulty: d, StopReason: stop, DurationMS: ms,
		Grade: &Grade{Solved: solved, Steps: steps, ToolCalls: tools, WastedCalls: wasted, USDMicros: usd},
	}
}

func TestPrintTable(t *testing.T) {
	results := []Result{
		gradedResult("stock-negative-reserve", "anthropic", Easy, true, "STOP_REASON_COMPLETED", 23, 41, 2, 420_000, 84_000),
		gradedResult("fx-rounding", "anthropic", Hard, false, "STOP_REASON_BUDGET_EXHAUSTED", 60, 95, 11, 1_234_567, 120_400),
		gradedResult("zz-openai-bug", "openai", Medium, true, "STOP_REASON_COMPLETED", 30, 50, 0, 310_000, 1000),
		{BugID: "journal-order", Provider: "openai", Difficulty: Hard, Error: "create sandbox: boom", DurationMS: 100},
	}
	rep := Report{Results: results, Summary: Summarize(results)}
	var buf bytes.Buffer
	PrintTable(&buf, rep)
	want := "BUG                     PROVIDER   RESULT  STOP              STEPS  TOOLS  WASTED  COST   SECONDS\n" +
		"stock-negative-reserve  anthropic  solved  COMPLETED         23     41     2       $0.42  84\n" +
		"fx-rounding             anthropic  failed  BUDGET_EXHAUSTED  60     95     11      $1.23  120\n" +
		"zz-openai-bug           openai     solved  COMPLETED         30     50     0       $0.31  1\n" +
		"journal-order           openai     error   -                 -      -      -       -      0\n" +
		"\n" +
		"anthropic  solved 1/2 (pass@1 0.500)  easy 1/1  medium 0/0  hard 0/1  median $0.42  median steps 23  total $1.65  errors 0\n" +
		"openai     solved 1/1 (pass@1 1.000)  easy 0/0  medium 1/1  hard 0/0  median $0.31  median steps 30  total $0.31  errors 1\n" +
		"note: results with errors are excluded from pass@1\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestPrintTableNoNoteWithoutErrors(t *testing.T) {
	results := []Result{gradedResult("a", "p", Easy, true, "STOP_REASON_COMPLETED", 1, 1, 0, 0, 0)}
	var buf bytes.Buffer
	PrintTable(&buf, Report{Results: results, Summary: Summarize(results)})
	if strings.Contains(buf.String(), "note:") {
		t.Errorf("unexpected note:\n%s", buf.String())
	}
}

func TestSolvedFalseWhenErrorSet(t *testing.T) {
	// Error must veto even a solved grade.
	if (Result{Error: "boom", Grade: &Grade{Solved: true}}).Solved() {
		t.Error("a result with an infrastructure error is solved")
	}
	if (Result{}).Solved() {
		t.Error("a result without a grade is solved")
	}
	if !(Result{Grade: &Grade{Solved: true}}).Solved() {
		t.Error("a graded solved result is not solved")
	}
}
