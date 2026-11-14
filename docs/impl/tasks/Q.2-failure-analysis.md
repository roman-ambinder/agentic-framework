# Q.2 — Failure analysis from traces: `adlc eval-report --failures`

Size: M · Depends on: — · Branch: `task/Q.2-failure-analysis` · PR title: `Q.2: label unsolved eval results with a failure cause`

## Goal

After this task `adlc eval-report REPORT.json --failures --db EVENTS.db`
labels every unsolved result of a report with exactly one failure cause,
computed from the result's grade and its run's events by a fixed, ordered
list of rules (first match wins), and prints how often each cause occurs per
agent plus one line per result with the cause and one line of evidence.
`--json` prints the same as JSON. The labels are the ones brief 1.10 uses by
hand, so automatic and manual analyses can be compared.

## Read first

- `AGENTS.md`, `docs/impl/m2.md`
- `docs/impl/tasks/1.10-coder-agent.md` — "Failure causes (use exactly these labels)"
- `internal/evals/runner.go` — `Result` (`Error`, `RunID`, `Hidden`, `Grade`; `State` is not in the JSON)
- `internal/evals/grade.go` — `Grade`, `Check`; `internal/evals/testoutput.go` — `TestOutcome`
- `internal/eventlog/fold.go` — `Fold`, `RunState` (`StopReason`, `ExhaustedBudget`, `StopDetail`, `Result`, `Step`)
- `internal/harness/loop.go`, `internal/harness/budget.go` — the exact stop details (`context budget exceeded`, `max_steps 60 reached`, …)
- `internal/cli/eval_report.go`, `internal/cli/openlog.go` (`openExistingLog`, `defaultDBPath`)

## Files

Create:
- `internal/evals/failures.go` — causes, `Classify`, `AnalyzeFailures`, `PrintFailures`
- `internal/evals/failures_test.go`

Change:
- `internal/cli/eval_report.go` — flags `--failures`, `--db`, `--json`
- `internal/cli/eval_report_test.go`
- `docs/impl/interfaces.md` §9 — the Q.2 line (see "Interfaces"), only if not already there

## Design

### Causes

```go
// FailureCause labels why an unsolved result failed. The names are brief 1.10's.
type FailureCause string

const (
	CauseInfra           FailureCause = "infra"
	CauseBudget          FailureCause = "budget"
	CauseContextTooLarge FailureCause = "context-too-large"
	CauseRefusedOrError  FailureCause = "refused-or-error"
	CauseToolMisuse      FailureCause = "tool-misuse"
	CauseGaveUp          FailureCause = "gave-up"
	CauseNoRepro         FailureCause = "no-repro"
	CauseBrokeBuild      FailureCause = "broke-build"
	CauseBrokeOtherTests FailureCause = "broke-other-tests"
	CausePartialFix      FailureCause = "partial-fix"
	CauseWrongDiagnosis  FailureCause = "wrong-diagnosis"
	CauseUnclassified    FailureCause = "unclassified"
)

// FailureCauses lists every cause in rule order.
var FailureCauses = []FailureCause{CauseInfra, CauseBudget, CauseContextTooLarge, CauseRefusedOrError,
	CauseToolMisuse, CauseGaveUp, CauseNoRepro, CauseBrokeBuild, CauseBrokeOtherTests,
	CausePartialFix, CauseWrongDiagnosis, CauseUnclassified}
```

All ten labels of brief 1.10 are used with their meaning narrowed to what the
data can show. Two are new: `context-too-large` (1.10 would file it under
`refused-or-error`, but its fix is different) and `unclassified` (no rule
matched; should not happen).

### Rules (first match wins)

`state` is `eventlog.Fold(events)` of the result's run; `g` is `r.Grade`.

| # | Cause | Condition | Evidence (one line) |
|---|-------|-----------|---------------------|
| 1 | `infra` | `r.Error != ""` (no events read) | `oneLine(r.Error)` |
| 1b | `infra` | `state.StopReason == STOP_REASON_CANCELLED` | `run cancelled` |
| 2 | `budget` | `state.StopReason == STOP_REASON_BUDGET_EXHAUSTED` | `<kind>: <state.StopDetail>`, kind = `strings.ToLower(strings.TrimPrefix(state.ExhaustedBudget.String(), "BUDGET_KIND_"))`, e.g. `steps: max_steps 60 reached` |
| 3 | `context-too-large` | `StopReason == STOP_REASON_ERROR && state.StopDetail == "context budget exceeded"` | `context budget exceeded at step <state.Step>` |
| 4 | `refused-or-error` | `StopReason` is `ERROR`, `DENIED` or `AWAITING_APPROVAL` | `<stop>: <detail>`, stop = `strings.ToLower(strings.TrimPrefix(name, "STOP_REASON_"))`; just `<stop>` when the detail is empty. E.g. `error: model refused` |
| 5 | `tool-misuse` | some tool failed 3 or more times in a row (streak below) | `<tool> failed <n> times in a row from seq <s>` |
| 6 | `unclassified` | `g == nil` | `result has no grade` |
| 7 | `gave-up` | `g.FilesChanged == 0` | `no files changed; final answer: "<first 80 bytes of state.Result>"`, or `no files changed; no final answer` when `state.Result == ""` |
| 8 | `no-repro` | `g.TestFilesChanged == 0` | `no _test.go file changed (<g.FilesChanged> files changed)` |
| 9 | `broke-build` | `!g.Build.OK` | `go build: <line>` (below), or `go build timed out`, or `go build exit <code>` |
| 10 | `broke-other-tests` | `!g.OtherTests.OK` | `go test: <line>` (below), or `go test timed out`, or `go test exit <code>` |
| 11 | `partial-fix` | `!g.HiddenPass && len(r.Hidden.Passed) > 0` | `hidden passed: A; failed: B` |
| 12 | `wrong-diagnosis` | `!g.HiddenPass` | `hidden failed: B` |
| 13 | `unclassified` | otherwise | `no rule matched` |

Rules 11–12: names joined with `", "`; append `; missing: C` when
`r.Hidden.Missing` is non-empty; when `Failed` is empty rule 12 prints
`hidden missing: C`.

Why each rule is computable, and what had to be adapted:
- 1.10's `no-repro` means "never ran a test that showed the bug". The log cannot show that a test run demonstrated the bug, so rule 8 uses "changed no `*_test.go` file". `Result.ChangedFiles` does not tell new files from edited ones, so an edited existing test counts as a test.
- 1.10's `wrong-diagnosis` ("changed the wrong code") and `partial-fix` are told apart by the hidden tests only: some passed → partial, none → wrong. The report does not hold the bug's `fix_files`, so "wrong code" is not checked.
- 1.10's `tool-misuse` ("most steps wasted") becomes the streak rule, which is exact and cheap.
- 1.10's `gave-up` ("ended without a working fix") becomes "changed nothing": every other COMPLETED-but-unsolved run fails one of rules 8–12.
- `budget` names which budget, from `RunState.ExhaustedBudget` and `StopDetail`; the report's `stop_reason` alone cannot.

### Streak (rule 5)

1. Build `name[callID]` from every `tool_call_requested` (`Call.Id` → `Call.Name`).
2. Walk the `tool_call_completed` events in seq order with `cur` (tool name), `n`, `from` (seq).
3. If `IsError` and `name[CallId] == cur`: `n++`. If `IsError` and the name differs: `cur, n, from = name, 1, seq`. If not an error: `cur, n = "", 0`.
4. The first time `n` reaches 3, remember `cur` and `from`, keep counting while the same streak goes on, and report its final length. Only the first streak that reaches 3 is reported.

Errors written by `adlc resume` (abandoned / not run) count like any error.

### Evidence lines

- `oneLine(s string) string`: replace every run of `\r` / `\n` with one space, `strings.TrimSpace`, and if longer than 160 bytes cut to 160 bytes at a UTF-8 boundary and append `...`. Every evidence string goes through it.
- Rule 9 `<line>`: the first line of `g.Build.Output` that is non-empty after `TrimSpace` and does not start with `#` (go build prints `# pkg` before the error).
- Rule 10 `<line>`: the first line of `g.OtherTests.Output` starting with `--- FAIL:`, else the first starting with `FAIL`, else fall back to timed out / exit.
- Rule 7: cut `state.Result` to 80 bytes at a UTF-8 boundary before quoting.

### API (`failures.go`)

```go
// Failure is the cause of one unsolved result.
type Failure struct {
	BugID    string       `json:"bug_id"`
	Agent    string       `json:"agent"`
	Provider string       `json:"provider"`
	RunID    string       `json:"run_id"`
	Cause    FailureCause `json:"cause"`
	Evidence string       `json:"evidence"`
}

// FailureReport is every failure of a report and the counts per agent.
type FailureReport struct {
	Failures []Failure                       `json:"failures"` // report order; never null
	ByAgent  map[string]map[FailureCause]int `json:"by_agent"` // agent id -> cause -> count (> 0 only); never null
}

// EventReader reads a run's events; *eventlog.Store implements it.
type EventReader interface {
	Read(ctx context.Context, runID string, afterSeq uint64) ([]*adlcv1.Event, error)
}

// Classify applies the rules to one unsolved result. events is the run's log;
// it is not read (and may be nil) when r.Error != "".
func Classify(r Result, events []*adlcv1.Event) (FailureCause, string, error)

// AnalyzeFailures classifies every result of rep that is not solved.
func AnalyzeFailures(ctx context.Context, rep Report, log EventReader) (FailureReport, error)

// PrintFailures writes the two tables of fr.
func PrintFailures(w io.Writer, fr FailureReport)
```

Errors (exact):
- `Classify`: a `Fold` failure → `fmt.Errorf("evals: failures: run %q: %w", r.RunID, err)`.
- `AnalyzeFailures`: a `Read` error → `fmt.Errorf("evals: failures: read run %q: %w", id, err)`; no events for a result without `Error` → `fmt.Errorf("evals: failures: %w: %q", eventlog.ErrRunNotFound, id)`. It stops at the first error.

### Text output (`PrintFailures`), full example

```
AGENT              CAUSE            COUNT
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
```

- Both tables use `tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)`, cells joined by `\t`, no `\t` after the last cell, flushed separately, with one blank line between them.
- Count rows: agents sorted by id; within an agent, count descending, ties in `FailureCauses` order.
- Result rows: in `Failures` order (= report order).
- No failures: the single line `failures: none`.

### `adlc eval-report`

New flags: `--failures` (bool), `--db` (default `defaultDBPath`), `--json` (bool).
- Without `--failures`: unchanged (`--db` is ignored). `--json` without `--failures` → exit 1, `eval-report: --json needs --failures`.
- With `--failures`: read the report (`readReport`); open the log with `openExistingLog` (a missing file → its error `event log <path> does not exist`, exit 1); `evals.AnalyzeFailures(ctx, rep, store)`; any error → exit 1.
  - text: `evals.PrintTable(stdout, rep)`, a blank line, `evals.PrintFailures(stdout, fr)`.
  - `--json`: only `json.MarshalIndent(fr, "", "  ")` and a newline.
- Use `cmd.Context()`; when it is nil use `context.Background()` (as `traceRun` does).

### Interfaces

Add to `interfaces.md` §9, after the 1.9 (or Q.1) line:
`// task Q.2: FailureCause and its Cause* constants, FailureCauses, Failure, FailureReport, EventReader, Classify, AnalyzeFailures, PrintFailures`

## Steps

1. Branch from the latest `main`.
2. Write the causes, `oneLine`, the streak and `Classify` with `TestClassify` first; see every row fail once.
3. `AnalyzeFailures` and its tests (an in-memory `EventReader`: `map[string][]*adlcv1.Event`).
4. `PrintFailures` and `TestPrintFailures`.
5. The flags in `eval_report.go` and the CLI tests.
6. `make fmt`, then Verify.

## Tests

`failures_test.go`. A helper `runLog(stop adlcv1.StopReason, detail string, kind adlcv1.BudgetKind, calls ...call)` builds a valid log for run `r1`: `run_started` (seq 1), for each `call{name string; isError bool}` a `tool_call_requested` (id `c<i>`) and a `tool_call_completed`, then `run_completed`; seqs 1..n. A helper `gradedResult(mod func(*Grade))` returns a graded, unsolved result (`FilesChanged 2`, `TestFilesChanged 1`, build and other tests OK, `HiddenPass false`, `Hidden.Failed ["TestHiddenX"]`) with `mod` applied.
- `TestClassify` — table, one row per rule, checks cause and exact evidence:
  - `Error: "create sandbox: boom\nmore"` → `infra`, `create sandbox: boom more`;
  - CANCELLED → `infra`, `run cancelled`;
  - BUDGET_EXHAUSTED, `BUDGET_KIND_STEPS`, `max_steps 60 reached` → `budget`, `steps: max_steps 60 reached`;
  - ERROR `context budget exceeded` → `context-too-large`;
  - ERROR `model refused` → `refused-or-error`, `error: model refused`; DENIED with empty detail → `denied`;
  - COMPLETED with `files__write_file` failing 4 times → `tool-misuse`, `files__write_file failed 4 times in a row from seq 3`;
  - `FilesChanged 0`, `state.Result "I could not find it"` → `gave-up`, `no files changed; final answer: "I could not find it"`;
  - `TestFilesChanged 0` → `no-repro`; `Build{OK: false, Output: "# ledger\nledger/a.go:3:1: syntax error\n"}` → `broke-build`, `go build: ledger/a.go:3:1: syntax error`;
  - `OtherTests` not OK with `--- FAIL: TestX (0.00s)` → `broke-other-tests`, `go test: --- FAIL: TestX (0.00s)`;
  - `Hidden.Passed ["A"], Failed ["B"]` → `partial-fix`, `hidden passed: A; failed: B`; `Failed ["B"], Missing ["C"]` → `wrong-diagnosis`, `hidden failed: B; missing: C`;
  - `Grade nil` → `unclassified`, `result has no grade`.
- `TestClassifyFirstMatchWins` — BUDGET_EXHAUSTED and a broken build → `budget`; a 3-error streak and `TestFilesChanged 0` → `tool-misuse`.
- `TestToolStreakNeedsSameToolInARow` — errors `a, a, ok, a, a` → no `tool-misuse`; `a, b, a, b` → none; `a, a, a` → `a failed 3 times in a row from seq 3`.
- `TestOneLine` — newlines collapse; 200 `é` (2 bytes each) → 160 bytes + `...`, valid UTF-8.
- `TestAnalyzeFailuresSkipsSolvedAndCounts` — 4 results (1 solved, 1 infra, 2 budget for agent `a@1`) → 3 failures in report order; `ByAgent["a@1"] == {budget: 2}`; the infra result's run id is never read (the reader fails the test if asked).
- `TestAnalyzeFailuresMissingRun` — an unsolved result whose run is not in the reader → `errors.Is(err, eventlog.ErrRunNotFound)`.
- `TestPrintFailures` — the `FailureReport` of the example as a literal → output equals the example exactly; an empty report → `failures: none\n`.

`eval_report_test.go` (a real `eventlog.Store` in `t.TempDir()`, events appended with `Append`):
- `TestEvalReportFailures` — one run ending BUDGET_EXHAUSTED (steps), a report with its result → code 0; stdout contains the existing summary line and `budget` and `steps: max_steps 3 reached`.
- `TestEvalReportFailuresJSON` — `--json` → stdout unmarshals into `evals.FailureReport` with one failure.
- `TestEvalReportJSONNeedsFailures` — code 1, stderr contains `eval-report: --json needs --failures`.
- `TestEvalReportFailuresMissingDB` — code 1, stderr contains `does not exist`.
- `TestEvalReportFailuresUnknownRun` — empty store, unsolved result → code 1, stderr contains `run not found`.

## Verify

```bash
make ci
```
Expected: passes.

```bash
go test ./internal/evals/ -run 'TestClassify|TestToolStreak|TestOneLine|TestAnalyzeFailures|TestPrintFailures' -v
go test ./internal/cli/ -run 'TestEvalReport' -v
```
Expected: every test PASS.

Live (after an `adlc eval` run):
```bash
./bin/adlc eval-report evals/results/<report>.json --failures --db .adlc/events.db
```
Expected: the normal table, a blank line, the cause counts and one row per unsolved result.

## Out of scope

- Model-graded or rubric-based failure analysis.
- Reading the suite (`fix_files`) to check that the right files were changed.
- Failure causes inside `adlc eval-compare` (task Q.1); the demo runs `--failures` on each report.
- Storing causes in the report file; they are computed on demand.

## Pitfalls

- First match wins: keep the rule order exactly; tests pin it.
- `Result.State` is `json:"-"`: in a report read from disk it is nil. Always fold the events from the log.
- Do not read events for results with `Error != ""`: their run may not exist.
- `Result.StopReason` is a string; the rules use `state.StopReason` (the enum) from the fold.
- A denied call has no `tool_call_completed`; it ends the run (rule 4), so it is never part of a streak.
- `Check.Output` holds only the last 4 KiB; the first matching line may be from the middle of the output. That is fine.
- Cut strings at UTF-8 boundaries (`utf8.RuneStart`), not at a raw byte index.

## Done checklist

- [ ] The 12 causes, `Classify`, `AnalyzeFailures`, `PrintFailures` exist as specified; labels match brief 1.10.
- [ ] `adlc eval-report --failures [--db] [--json]` works; plain `eval-report` is unchanged.
- [ ] All tests listed exist and pass; each was seen failing once.
- [ ] `interfaces.md` §9 has the Q.2 line.
- [ ] `make ci` passes.
