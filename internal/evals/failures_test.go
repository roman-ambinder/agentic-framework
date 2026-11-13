package evals

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

type call struct {
	name    string
	isError bool
}

func runLogFor(runID, result string, stop adlcv1.StopReason, detail string, kind adlcv1.BudgetKind, calls ...call) []*adlcv1.Event {
	var evs []*adlcv1.Event
	add := func(ev *adlcv1.Event) {
		ev.RunId = runID
		ev.Seq = uint64(len(evs) + 1)
		evs = append(evs, ev)
	}
	add(&adlcv1.Event{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{Task: "t"}}})
	for i, c := range calls {
		id := "c" + string(rune('0'+i))
		add(&adlcv1.Event{Payload: &adlcv1.Event_ToolCallRequested{ToolCallRequested: &adlcv1.ToolCallRequested{
			Call: &adlcv1.ToolCall{Id: id, Name: c.name},
		}}})
		add(&adlcv1.Event{Payload: &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{
			CallId: id, IsError: c.isError,
		}}})
	}
	add(&adlcv1.Event{Payload: &adlcv1.Event_RunCompleted{RunCompleted: &adlcv1.RunCompleted{
		StopReason: stop, Detail: detail, ExhaustedBudget: kind, Result: result,
	}}})
	return evs
}

func runLog(stop adlcv1.StopReason, detail string, kind adlcv1.BudgetKind, calls ...call) []*adlcv1.Event {
	return runLogFor("r1", "", stop, detail, kind, calls...)
}

func failedResult(mod func(*Grade)) Result {
	g := &Grade{FilesChanged: 2, TestFilesChanged: 1, Build: Check{OK: true}, OtherTests: Check{OK: true}}
	if mod != nil {
		mod(g)
	}
	return Result{
		BugID: "b", Agent: "a@1", RunID: "r1", Grade: g,
		Hidden: TestOutcome{Failed: []string{"TestHiddenX"}},
	}
}

const (
	completedStop = adlcv1.StopReason_STOP_REASON_COMPLETED
	noKind        = adlcv1.BudgetKind_BUDGET_KIND_UNSPECIFIED
)

func TestClassify(t *testing.T) {
	failing := []call{{"files__write_file", true}, {"files__write_file", true}, {"files__write_file", true}, {"files__write_file", true}}
	tests := []struct {
		name      string
		r         Result
		events    []*adlcv1.Event
		wantCause FailureCause
		wantEv    string
	}{
		{"infra error", Result{Error: "create sandbox: boom\nmore"}, nil, CauseInfra, "create sandbox: boom more"},
		{"cancelled", failedResult(nil), runLog(adlcv1.StopReason_STOP_REASON_CANCELLED, "", noKind), CauseInfra, "run cancelled"},
		{
			"budget", failedResult(nil), runLog(adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED, "max_steps 60 reached", adlcv1.BudgetKind_BUDGET_KIND_STEPS),
			CauseBudget, "steps: max_steps 60 reached",
		},
		{
			"context", failedResult(nil), runLog(adlcv1.StopReason_STOP_REASON_ERROR, "context budget exceeded", noKind),
			CauseContextTooLarge, "context budget exceeded at step 0",
		},
		{
			"error", failedResult(nil), runLog(adlcv1.StopReason_STOP_REASON_ERROR, "model refused", noKind),
			CauseRefusedOrError, "error: model refused",
		},
		{"denied", failedResult(nil), runLog(adlcv1.StopReason_STOP_REASON_DENIED, "", noKind), CauseRefusedOrError, "denied"},
		{
			"tool misuse", failedResult(nil), runLog(completedStop, "", noKind, failing...), CauseToolMisuse,
			"files__write_file failed 4 times in a row from seq 3",
		},
		{"no grade", Result{RunID: "r1"}, runLog(completedStop, "", noKind), CauseUnclassified, "result has no grade"},
		{
			"gave up", failedResult(func(g *Grade) { g.FilesChanged = 0 }),
			runLogFor("r1", "I could not find it", completedStop, "", noKind), CauseGaveUp,
			`no files changed; final answer: "I could not find it"`,
		},
		{
			"gave up silent", failedResult(func(g *Grade) { g.FilesChanged = 0 }), runLog(completedStop, "", noKind),
			CauseGaveUp, "no files changed; no final answer",
		},
		{
			"no repro", failedResult(func(g *Grade) { g.TestFilesChanged = 0 }), runLog(completedStop, "", noKind),
			CauseNoRepro, "no _test.go file changed (2 files changed)",
		},
		{"broke build", failedResult(func(g *Grade) {
			g.Build = Check{Output: "# ledger\nledger/a.go:3:1: syntax error\n"}
		}), runLog(completedStop, "", noKind), CauseBrokeBuild, "go build: ledger/a.go:3:1: syntax error"},
		{
			"broke build timeout", failedResult(func(g *Grade) { g.Build = Check{TimedOut: true} }), runLog(completedStop, "", noKind),
			CauseBrokeBuild, "go build timed out",
		},
		{
			"broke build exit", failedResult(func(g *Grade) { g.Build = Check{ExitCode: 2} }), runLog(completedStop, "", noKind),
			CauseBrokeBuild, "go build exit 2",
		},
		{"broke other tests", failedResult(func(g *Grade) {
			g.OtherTests = Check{Output: "FAIL\n--- FAIL: TestX (0.00s)\n"}
		}), runLog(completedStop, "", noKind), CauseBrokeOtherTests, "go test: --- FAIL: TestX (0.00s)"},
		{"broke other tests FAIL line", failedResult(func(g *Grade) {
			g.OtherTests = Check{Output: "ok x\nFAIL\tpkg\t0.1s\n"}
		}), runLog(completedStop, "", noKind), CauseBrokeOtherTests, "go test: FAIL\tpkg\t0.1s"},
		{
			"broke other tests exit", failedResult(func(g *Grade) { g.OtherTests = Check{ExitCode: 1} }), runLog(completedStop, "", noKind),
			CauseBrokeOtherTests, "go test exit 1",
		},
		{"partial fix", func() Result {
			r := failedResult(nil)
			r.Hidden = TestOutcome{Passed: []string{"A"}, Failed: []string{"B"}}
			return r
		}(), runLog(completedStop, "", noKind), CausePartialFix, "hidden passed: A; failed: B"},
		{"wrong diagnosis missing", func() Result {
			r := failedResult(nil)
			r.Hidden = TestOutcome{Failed: []string{"B"}, Missing: []string{"C"}}
			return r
		}(), runLog(completedStop, "", noKind), CauseWrongDiagnosis, "hidden failed: B; missing: C"},
		{"wrong diagnosis only missing", func() Result {
			r := failedResult(nil)
			r.Hidden = TestOutcome{Missing: []string{"C"}}
			return r
		}(), runLog(completedStop, "", noKind), CauseWrongDiagnosis, "hidden missing: C"},
		{
			"no rule matched", failedResult(func(g *Grade) { g.HiddenPass = true }), runLog(completedStop, "", noKind),
			CauseUnclassified, "no rule matched",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cause, ev, err := Classify(tt.r, tt.events)
			if err != nil {
				t.Fatal(err)
			}
			if cause != tt.wantCause || ev != tt.wantEv {
				t.Errorf("got (%s, %q), want (%s, %q)", cause, ev, tt.wantCause, tt.wantEv)
			}
		})
	}
}

func TestClassifyBadLog(t *testing.T) {
	_, _, err := Classify(failedResult(nil), nil)
	if !errors.Is(err, eventlog.ErrInvalidLog) || !strings.Contains(err.Error(), `evals: failures: run "r1"`) {
		t.Errorf("err = %v", err)
	}
}

func TestClassifyFirstMatchWins(t *testing.T) {
	broken := func(g *Grade) { g.Build = Check{Output: "x"} }
	cause, _, _ := Classify(failedResult(broken), runLog(adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED, "d", adlcv1.BudgetKind_BUDGET_KIND_STEPS))
	if cause != CauseBudget {
		t.Errorf("cause = %s, want budget", cause)
	}
	streak := []call{{"a", true}, {"a", true}, {"a", true}}
	cause, _, _ = Classify(failedResult(func(g *Grade) { g.TestFilesChanged = 0 }), runLog(completedStop, "", noKind, streak...))
	if cause != CauseToolMisuse {
		t.Errorf("cause = %s, want tool-misuse", cause)
	}
}

func TestToolStreakNeedsSameToolInARow(t *testing.T) {
	a, b, ok := func(n string) call { return call{n, true} }, func(n string) call { return call{n, true} }, call{"a", false}
	tests := []struct {
		name  string
		calls []call
		want  string // "" = none
	}{
		{"broken by success", []call{a("a"), a("a"), ok, a("a"), a("a")}, ""},
		{"alternating", []call{a("a"), b("b"), a("a"), b("b")}, ""},
		{"three", []call{a("a"), a("a"), a("a")}, "a failed 3 times in a row from seq 3"},
		{"after other tool", []call{b("b"), a("a"), a("a"), a("a"), a("a"), ok, a("a")}, "a failed 4 times in a row from seq 5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cause, ev, err := Classify(failedResult(nil), runLog(completedStop, "", noKind, tt.calls...))
			if err != nil {
				t.Fatal(err)
			}
			if tt.want == "" {
				if cause == CauseToolMisuse {
					t.Errorf("unexpected tool-misuse: %s", ev)
				}
				return
			}
			if cause != CauseToolMisuse || ev != tt.want {
				t.Errorf("got (%s, %q), want %q", cause, ev, tt.want)
			}
		})
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\r\n\nb \n"); got != "a b" {
		t.Errorf("got %q", got)
	}
	got := oneLine(strings.Repeat("é", 200))
	if !utf8.ValidString(got) || got != strings.Repeat("é", 80)+"..." || len(got) != 163 {
		t.Errorf("len %d, valid %v", len(got), utf8.ValidString(got))
	}
	// A cut that would fall inside a rune moves back.
	got = oneLine("x" + strings.Repeat("é", 100))
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "...") || len(got) != 159+3 {
		t.Errorf("len %d, valid %v", len(got), utf8.ValidString(got))
	}
}

type mapReader struct {
	t    *testing.T
	logs map[string][]*adlcv1.Event
}

func (m mapReader) Read(_ context.Context, id string, _ uint64) ([]*adlcv1.Event, error) {
	if id == "infra-run" {
		m.t.Errorf("read the run of a result with Error")
	}
	return m.logs[id], nil
}

func TestAnalyzeFailuresSkipsSolvedAndCounts(t *testing.T) {
	budget := func(id string) []*adlcv1.Event {
		return runLogFor(id, "", adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED, "max_steps 3 reached", adlcv1.BudgetKind_BUDGET_KIND_STEPS)
	}
	rep := Report{Results: []Result{
		{BugID: "b1", Agent: "a@1", RunID: "ok", Grade: &Grade{Solved: true}},
		{BugID: "b2", Agent: "a@1", RunID: "infra-run", Error: "boom"},
		{BugID: "b3", Agent: "a@1", RunID: "r3", Grade: &Grade{}},
		{BugID: "b4", Agent: "a@1", RunID: "r4", Grade: &Grade{}},
	}}
	fr, err := AnalyzeFailures(context.Background(), rep, mapReader{t, map[string][]*adlcv1.Event{"r3": budget("r3"), "r4": budget("r4")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.Failures) != 3 || fr.Failures[0].BugID != "b2" || fr.Failures[1].BugID != "b3" || fr.Failures[2].BugID != "b4" {
		t.Fatalf("failures = %+v", fr.Failures)
	}
	if fr.Failures[0].Cause != CauseInfra || fr.Failures[1].Evidence != "steps: max_steps 3 reached" {
		t.Errorf("failures = %+v", fr.Failures)
	}
	got := fr.ByAgent["a@1"]
	if len(got) != 2 || got[CauseBudget] != 2 || got[CauseInfra] != 1 {
		t.Errorf("by agent = %v", fr.ByAgent)
	}
}

func TestAnalyzeFailuresEmptyIsNotNull(t *testing.T) {
	fr, err := AnalyzeFailures(context.Background(), Report{}, mapReader{t, nil})
	if err != nil || fr.Failures == nil || fr.ByAgent == nil {
		t.Errorf("fr = %+v, err = %v", fr, err)
	}
}

func TestAnalyzeFailuresMissingRun(t *testing.T) {
	rep := Report{Results: []Result{{BugID: "b", Agent: "a@1", RunID: "gone", Grade: &Grade{}}}}
	_, err := AnalyzeFailures(context.Background(), rep, mapReader{t, nil})
	if !errors.Is(err, eventlog.ErrRunNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestPrintFailures(t *testing.T) {
	fr := FailureReport{
		ByAgent: map[string]map[FailureCause]int{
			"coder-anthropic@1": {CauseBudget: 1, CauseWrongDiagnosis: 2},
			"coder-openai@1":    {CauseBrokeBuild: 1, CauseInfra: 1},
		},
		Failures: []Failure{
			{"ledger-balance-asof", "coder-anthropic@1", "anthropic", "eval-ledger-balance-asof-anthropic-20261113T101502-118", CauseWrongDiagnosis, "hidden failed: TestHiddenBalanceIncludesAsOfDay"},
			{"ledger-balance-asof", "coder-openai@1", "openai", "eval-ledger-balance-asof-openai-20261113T104411-302", CauseBrokeBuild, "go build: ledger/balance.go:41:2: undefined: asOfEnd"},
			{"money-split-remainder", "coder-anthropic@1", "anthropic", "eval-money-split-remainder-anthropic-20261113T102233-907", CauseWrongDiagnosis, "hidden failed: TestHiddenSplitRemainder"},
			{"money-split-remainder", "coder-openai@1", "openai", "eval-money-split-remainder-openai-20261113T105020-001", CauseInfra, "create sandbox: docker: connection refused"},
			{"statement-opening-sign", "coder-anthropic@1", "anthropic", "eval-statement-opening-sign-anthropic-20261113T103010-455", CauseBudget, "steps: max_steps 60 reached"},
		},
	}
	want := `AGENT              CAUSE            COUNT
coder-anthropic@1  wrong-diagnosis  2
coder-anthropic@1  budget           1
coder-openai@1     infra            1
coder-openai@1     broke-build      1

BUG                     AGENT              CAUSE            RUN                                                        EVIDENCE
ledger-balance-asof     coder-anthropic@1  wrong-diagnosis  eval-ledger-balance-asof-anthropic-20261113T101502-118     hidden failed: TestHiddenBalanceIncludesAsOfDay
ledger-balance-asof     coder-openai@1     broke-build      eval-ledger-balance-asof-openai-20261113T104411-302        go build: ledger/balance.go:41:2: undefined: asOfEnd
money-split-remainder   coder-anthropic@1  wrong-diagnosis  eval-money-split-remainder-anthropic-20261113T102233-907   hidden failed: TestHiddenSplitRemainder
money-split-remainder   coder-openai@1     infra            eval-money-split-remainder-openai-20261113T105020-001      create sandbox: docker: connection refused
statement-opening-sign  coder-anthropic@1  budget           eval-statement-opening-sign-anthropic-20261113T103010-455  steps: max_steps 60 reached
`
	var buf bytes.Buffer
	PrintFailures(&buf, fr)
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
	buf.Reset()
	PrintFailures(&buf, FailureReport{})
	if buf.String() != "failures: none\n" {
		t.Errorf("empty = %q", buf.String())
	}
}
