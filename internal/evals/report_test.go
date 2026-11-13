package evals

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestPrintTable(t *testing.T) {
	var buf bytes.Buffer
	PrintTable(&buf, sampleResults())
	want := "BUG                     PROVIDER   RESULT  STOP              SECONDS\n" +
		"zz-first-row-openai     openai     solved  COMPLETED         1\n" +
		"stock-negative-reserve  anthropic  solved  COMPLETED         84\n" +
		"fx-rounding             openai     failed  BUDGET_EXHAUSTED  120\n" +
		"journal-order           anthropic  error   -                 0\n" +
		"anthropic: 1/2 solved\n" +
		"openai: 1/2 solved\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestSolvedFalseWhenErrorSet(t *testing.T) {
	// An empty TestOutcome counts as AllPassed, so Error must veto.
	if (Result{Error: "boom"}).Solved() {
		t.Error("a result with an infrastructure error is solved")
	}
	if !(Result{}).Solved() {
		t.Error("an empty result without error should follow AllPassed")
	}
}
