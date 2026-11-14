# Q.1 — Repeated eval runs, pass@k and `adlc eval-compare`

Size: M · Depends on: — · Branch: `task/Q.1-eval-compare` · PR title: `Q.1: eval --repeat, pass@k and adlc eval-compare`

## Goal

After this task `adlc eval --repeat N` runs every bug × agent N times, one
after the other, each attempt with its own run id. Every result records its
attempt number, and the report summary gains, per agent, the attempts per bug
and pass@k for k = 1..N (unbiased estimator). `adlc eval-compare BASE.json
NEW.json` prints, per agent pair and per difficulty, pass@1 (and pass@k when
both reports have repeats), median cost and median steps of both reports with
deltas, and the bugs whose status changed. Reports written before this task
still load: a result without an attempt number is one attempt.

## Read first

- `AGENTS.md`, `docs/impl/m2.md` (decisions 4 and 6)
- `docs/impl/tasks/1.8-eval-runner.md`, `docs/impl/tasks/1.9-graders.md`
- `internal/evals/runner.go` — `Runner`, `Result`, `RunAll`, `RunOne`, `runID`
- `internal/evals/summary.go` — `Summarize`, `median`, `medianFloat`, `finish` (rounding to 3 decimals)
- `internal/evals/report.go` — `Report`, `PrintTable`, `printSummary`, `usd`
- `internal/cli/eval.go` — `evalOptions`, `runEval`, `evalDryRun`; `internal/cli/eval_report.go` — `readReport`

## Files

Change:
- `internal/evals/runner.go` — `Runner.Repeat`, `Result.Attempt`, `RunAll` loop, run id suffix
- `internal/evals/summary.go` — `PassAtK`, `BugAttempts`, `AgentBucket`, `AgentSummary`, `Summary.ByAgent`, `SummarizeAgents`
- `internal/evals/report.go` — `Report.Repeat`; pass@k lines in `printSummary`
- `internal/cli/eval.go` — `--repeat` flag, dry-run worst case × N
- `internal/cli/root.go` — add `root.AddCommand(newEvalCompareCmd())` (one added line)
- `internal/evals/runner_test.go`, `summary_test.go`, `report_test.go`, `internal/cli/eval_test.go`
- `docs/impl/interfaces.md` §9 — the Q.1 line (see "Interfaces"), only if it is not already there

Create:
- `internal/evals/compare.go`, `internal/evals/compare_test.go`
- `internal/cli/eval_compare.go`, `internal/cli/eval_compare_test.go`

## Design

### Repeats (`runner.go`)

```go
// in Runner:
	Repeat int // attempts per bug x agent; 0 or 1 = one attempt

// in Result, after RunID:
	Attempt int `json:"attempt"` // 1-based; 0 in reports written before task Q.1 (read as 1)
```

`RunAll` loops bugs (outer), agents, then attempts `1..max(1, r.Repeat)`
(inner), sequentially, checking `ctx.Err()` before every attempt as today.
`RunOne(ctx, bug, agent)` keeps its signature and becomes
`return r.runAttempt(ctx, bug, agent, 1)`; `runAttempt` is the old body plus
`res.Attempt = attempt`, and when `r.Repeat > 1` the run id gets the suffix
`fmt.Sprintf("-a%d", attempt)`:
`eval-ok-001-anthropic-20261113T101502-118-a2`. With `Repeat <= 1` run ids
are unchanged. Progress lines are unchanged.

`Report` gets `Repeat int \`json:"repeat,omitempty"\`` (0 in old reports; read as 1).

### pass@k (`summary.go`)

```go
// PassAtK is the unbiased pass@k estimate for n attempts of which c solved:
// 1 - C(n-c, k) / C(n, k). It returns 0 unless 1 <= k <= n and 0 <= c <= n.
func PassAtK(n, c, k int) float64
```

Algorithm (no big binomials): 1. If the inputs are out of range, return 0.
2. If `n-c < k`, return 1. 3. `p := 1.0`; for `i := n-c+1; i <= n; i++ { p *= 1 - float64(k)/float64(i) }`. 4. Return `1 - p`.

```go
// BugAttempts counts one agent's attempts on one bug.
type BugAttempts struct {
	BugID      string     `json:"bug_id"`
	Difficulty Difficulty `json:"difficulty"`
	Attempts   int        `json:"attempts"` // results, including errors
	Graded     int        `json:"graded"`   // results with Error == ""
	Solved     int        `json:"solved"`   // results with Solved()
}

// AgentBucket aggregates one agent over a set of bugs.
type AgentBucket struct {
	BugCount              int       `json:"bug_count"` // bugs with Graded >= 1
	Graded                int       `json:"graded"`
	Solved                int       `json:"solved"`
	PassAtK               []float64 `json:"pass_at_k"` // [k-1] = pass@k, k = 1..MaxAttempts; 3 decimals
	MedianUSDMicrosSolved int64     `json:"median_usd_micros_solved"`
	MedianStepsSolved     float64   `json:"median_steps_solved"`
}

// AgentSummary aggregates one agent's results.
type AgentSummary struct {
	AgentBucket                                         // all difficulties
	Provider     string                     `json:"provider"`
	MaxAttempts  int                        `json:"max_attempts"` // most Attempts of any bug
	Errors       int                        `json:"errors"`
	ByDifficulty map[Difficulty]AgentBucket `json:"by_difficulty"` // always easy, medium, hard
	PerBug       []BugAttempts              `json:"per_bug"`       // sorted by BugID
}

// SummarizeAgents summarizes results per agent id (Result.Agent).
func SummarizeAgents(results []Result) map[string]AgentSummary
```

`Summary` gets `ByAgent map[string]AgentSummary \`json:"by_agent,omitempty"\``;
`Summarize` sets it to `SummarizeAgents(results)`. `ProviderSummary` and
`Bucket` do not change (their `PassAt1` counts attempts, not bugs; the
agent summary is the one to use with repeats).

`SummarizeAgents` algorithm, per agent id:
1. Walk the results in order. Per `BugID` keep a `BugAttempts` (Difficulty from the first result of that bug): `Attempts++`; if `Error == ""` then `Graded++`; if `Solved()` then `Solved++`. If `Error != ""` then `Errors++`. `Provider` = first result's provider.
2. For every solved result with `Grade != nil`, append `Grade.USDMicros` and `Grade.Steps` to the agent's lists and to the lists of its difficulty.
3. `MaxAttempts` = the largest `Attempts`. `PerBug` sorted by `BugID`.
4. `bucketOf(bugs, usd, steps, maxK)`: `BugCount` = bugs with `Graded >= 1`; `Graded`, `Solved` = sums; for k = 1..maxK, `PassAtK[k-1]` = mean of `PassAtK(b.Graded, b.Solved, k)` over the bugs with `b.Graded >= k`, rounded like `finish` (`math.Round(x*1000)/1000`), 0 when no bug qualifies; medians with `median` / `medianFloat` (empty → 0).
5. `AgentBucket` = `bucketOf(all bugs, …)`; `ByDifficulty[d]` = `bucketOf(bugs of d, lists of d, …)` for easy, medium, hard, always with `maxK = MaxAttempts`.

Old reports: results have `Attempt == 0` and one result per bug × agent, so
`MaxAttempts == 1` and `PassAtK == [Solved/Graded per bug, averaged]`. Never
read `Attempt` in the summary; count results.

### pass@k lines in the table (`printSummary`)

After the provider lines and before the errors note, for every agent (sorted
by id) whose `MaxAttempts > 1`, print
`fmt.Sprintf("%-*s  attempts %d  %s\n", width, id, s.MaxAttempts, strings.Join(parts, "  "))`
where `parts[k-1] = fmt.Sprintf("pass@%d %.3f", k, s.PassAtK[k-1])` and
`width` is the longest printed agent id:

```
coder-anthropic@1  attempts 3  pass@1 0.500  pass@2 0.633  pass@3 0.700
coder-openai@1     attempts 3  pass@1 0.433  pass@2 0.567  pass@3 0.600
```

Without repeats nothing is added, so the existing `TestPrintTable` output is unchanged.

### `adlc eval --repeat N`

Flag `f.IntVar(&o.repeat, "repeat", 1, "attempts per bug and agent (1 to 20)")`.
First thing in `runEval`: if `o.repeat < 1 || o.repeat > 20` return exit 1 with
`eval: --repeat must be between 1 and 20`. Set `Runner.Repeat = o.repeat` and
`rep.Repeat = o.repeat`. `evalDryRun(stdout, bugs, agents, repeat)` multiplies
the total by `repeat` and prints
`bugs=%d agents=%d repeat=%d worst_case_usd=%d.%06d\n`, for example
`bugs=1 agents=2 repeat=3 worst_case_usd=7.500000`.

### Matching agents (`compare.go`)

```go
// AgentPair is a base agent id compared with a new agent id.
type AgentPair struct {
	Base string `json:"base"`
	New  string `json:"new"`
}

// AgentName returns id without its last "@<version>" ("coder@2" -> "coder"); id itself if it has no "@".
func AgentName(id string) string
```

Agent ids in a report = the distinct `Result.Agent` values.
- **No mapping given:** agents are matched by `AgentName`. If two ids in one report share a name, return `evals: compare: base report has agents "coder@1" and "coder@2" with the same name; use --map` (`new report` for the other side; the two ids in sorted order). Pairs = names present in both, sorted by name. No pair → `evals: compare: no agent name appears in both reports; use --map BASE_AGENT=NEW_AGENT`.
- **Mapping given** (`--map BASE=NEW`, repeatable): only the mapped pairs are compared, in flag order, matched by exact agent id. An id missing from its report → `evals: compare: --map: agent "x@1" is not in the base report` (or `new report`).
- Ids not in any pair go to `BaseOnlyAgents` / `NewOnlyAgents` (sorted, never null).

### Comparison types and algorithm

```go
// ReportInfo describes one input report.
type ReportInfo struct {
	Path    string `json:"path"` // set by the caller; Compare leaves it empty
	Suite   string `json:"suite"`
	Results int    `json:"results"`
	Repeat  int    `json:"repeat"` // Report.Repeat, 0 read as 1
}

// CompareRow is one difficulty, or "all", of one pair.
type CompareRow struct {
	Difficulty string      `json:"difficulty"` // "easy", "medium", "hard" or "all"
	Bugs       int         `json:"bugs"`
	Base       AgentBucket `json:"base"`
	New        AgentBucket `json:"new"`
}

// BugChange is a bug whose status differs between the reports.
type BugChange struct {
	BugID      string      `json:"bug_id"`
	Difficulty Difficulty  `json:"difficulty"`
	Base       BugAttempts `json:"base"`
	New        BugAttempts `json:"new"`
	BaseStatus string      `json:"base_status"`
	NewStatus  string      `json:"new_status"`
}

// PairComparison compares one pair on the bugs both reports have for it.
type PairComparison struct {
	AgentPair
	K            int          `json:"k"` // pass@k column; 0 = pass@1 only
	Rows         []CompareRow `json:"rows"`
	Changes      []BugChange  `json:"changes"`        // sorted by bug id; never null
	BaseOnlyBugs []string     `json:"base_only_bugs"` // sorted; never null
	NewOnlyBugs  []string     `json:"new_only_bugs"`
}

// Comparison is the result of Compare.
type Comparison struct {
	Base           ReportInfo       `json:"base"`
	New            ReportInfo       `json:"new"`
	BaseOnlyAgents []string         `json:"base_only_agents"`
	NewOnlyAgents  []string         `json:"new_only_agents"`
	Pairs          []PairComparison `json:"pairs"`
}

// BugStatus is "error" when b.Graded == 0, "solved" when 2*b.Solved > b.Graded, else "unsolved".
func BugStatus(b BugAttempts) string

// Compare matches the agents of the two reports and compares each pair.
func Compare(base, newRep Report, mapping []AgentPair) (Comparison, error)

// PrintComparison writes the text form of c.
func PrintComparison(w io.Writer, c Comparison)
```

`Compare`, per pair:
1. `rb` = base results with `Agent == pair.Base`; `rn` = new results with `Agent == pair.New`.
2. Common bugs = bug ids with at least one result in `rb` and in `rn`. The others go to `BaseOnlyBugs` / `NewOnlyBugs`. Drop them from `rb`, `rn`.
3. If there is no common bug: `Rows` and `Changes` empty, stop.
4. `sb := SummarizeAgents(rb)[pair.Base]`, `sn := SummarizeAgents(rn)[pair.New]` — always from the results, never from the stored summary (old reports have none).
5. `K = min(sb.MaxAttempts, sn.MaxAttempts)`; if `K < 2` then `K = 0`.
6. Rows: for d in easy, medium, hard, if some `sb.PerBug` has difficulty d: `{d, that count, sb.ByDifficulty[d], sn.ByDifficulty[d]}`. Then always `{"all", len(common), sb.AgentBucket, sn.AgentBucket}`.
7. Changes: for each common bug (sorted), if `BugStatus` differs, append a `BugChange` (Difficulty from the base side).

### Text output (`PrintComparison`), full example

```
base: evals/results/it0.json  suite go-bugs  results 60  repeat 3
new:  evals/results/it1.json  suite go-bugs  results 60  repeat 3
only in base: coder-openai@1
only in new: coder-openai-plan@1

coder-anthropic@1 -> coder-anthropic@2  bugs 10
DIFFICULTY  BUGS  PASS@1                   PASS@3                   MEDIAN COST              MEDIAN STEPS
easy        3     0.889 -> 1.000 (+0.111)  1.000 -> 1.000 (+0.000)  $0.21 -> $0.18 (-$0.03)  14 -> 12 (-2)
medium      4     0.500 -> 0.583 (+0.083)  0.750 -> 1.000 (+0.250)  $0.40 -> $0.44 (+$0.04)  25 -> 27 (+2)
hard        3     0.111 -> 0.000 (-0.111)  0.333 -> 0.000 (-0.333)  $0.95 -> - (n/a)         41 -> - (n/a)
all         10    0.500 -> 0.533 (+0.033)  0.700 -> 0.700 (+0.000)  $0.33 -> $0.30 (-$0.03)  20 -> 18.5 (-1.5)
status changes:
  importcsv-parentheses  solved 3/3 -> unsolved 1/3
  ledger-balance-asof    unsolved 1/3 -> solved 3/3
```

Rules (in print order):
1. `base: %s  suite %s  results %d  repeat %d` and `new:  %s  …` (two spaces after `new:`).
2. If the suites differ: `warning: the reports are from different suites`.
3. `only in base: <ids joined ", ">` / `only in new: …`, each only when non-empty.
4. Per pair: a blank line, then `<base> -> <new>  bugs <common>`; then `note: bugs only in base: a, b` / `note: bugs only in new: c` when non-empty. With no common bug print `no bug has results in both reports` and go to the next pair.
5. The table, through `tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)`, cells separated by `\t`, no `\t` after the last cell. The `PASS@K` column (header `fmt.Sprintf("PASS@%d", K)`) only when `K > 0`.
   - pass cell: `fmt.Sprintf("%.3f -> %.3f (%+.3f)", b, n, n-b)` with `b = Base.PassAtK[k-1]`, `n = New.PassAtK[k-1]` (0 if the slice is shorter).
   - cost cell: each side `usd(MedianUSDMicrosSolved)`, or `-` when that side's `Solved == 0`; delta `(+$0.04)` / `(-$0.03)` (`+` for >= 0, amount `usd(abs)`), or `(n/a)` when either side is `-`.
   - steps cell: each side `strconv.FormatFloat(x, 'f', -1, 64)` or `-`; delta `+`/`-` then the absolute value formatted the same way, or `(n/a)`.
6. `status changes:` followed by one line per change, through a second tabwriter: `"  " + bug + "\t" + side(base) + " -> " + side(new)`, where `side` is `solved 3/3` / `unsolved 1/3` (`<status> <Solved>/<Graded>`) or `error`. No change: the single line `status changes: none`.

`adlc eval-compare BASE.json NEW.json [--map BASE=NEW ...] [--json]`
(`internal/cli/eval_compare.go`, `cobra.ExactArgs(2)`): reads both with
`readReport` (its errors already say `read report <path>: …`), parses each
`--map` value (`f.StringArrayVar`) by splitting at the first `=`; an empty side
→ `eval-compare: --map "x": want BASE_AGENT=NEW_AGENT`. Calls `evals.Compare`,
sets `c.Base.Path`, `c.New.Path` to the arguments, then prints with
`PrintComparison`, or with `--json` writes `json.MarshalIndent(c, "", "  ")`
plus a newline. Every error → exit 1. Exit 0 otherwise (it is a report, not a gate).

### Interfaces

Add to `interfaces.md` §9, after the 1.9 line:
`// task Q.1: Runner.Repeat, Result.Attempt, Report.Repeat, PassAtK, BugAttempts, AgentBucket, AgentSummary, Summary.ByAgent, SummarizeAgents, AgentPair, AgentName, ReportInfo, CompareRow, BugChange, PairComparison, Comparison, BugStatus, Compare, PrintComparison`

## Steps

1. Branch from the latest `main`.
2. `PassAtK` and `TestPassAtK`.
3. `BugAttempts`, `AgentBucket`, `AgentSummary`, `SummarizeAgents`, `Summary.ByAgent`; their tests. Run the existing summary and report tests: they must pass unchanged.
4. `Runner.Repeat`, `Result.Attempt`, `runAttempt`, `RunAll`; runner tests.
5. `Report.Repeat`; pass@k lines in `printSummary`; report tests.
6. `--repeat` and the dry run in `eval.go`; update `TestEvalDryRunWorstCase` to the new line (`repeat=1`).
7. `compare.go` (`AgentName`, matching, `Compare`, `BugStatus`, `PrintComparison`) and `compare_test.go`.
8. `eval_compare.go`, register it in `root.go`, `eval_compare_test.go`.
9. `make fmt`, then Verify.

## Tests

`summary_test.go`:
- `TestPassAtK` — table, tolerance 1e-9: (3,0,1)=0; (3,3,1)=1; (3,1,1)=1/3; (3,1,2)=2/3; (3,1,3)=1; (5,2,2)=0.7; (4,0,4)=0; (1,1,1)=1; (2,1,3)=0 (k > n); (2,1,0)=0.
- `TestSummarizeAgentsPassAtK` — agent `a@1`: bug `x` (easy) 3 results, 1 solved; bug `y` (medium) 3 results, 3 solved → `MaxAttempts 3`, `PassAtK [0.667 0.833 1]`, `ByDifficulty[Easy].PassAtK [0.333 0.667 1]`, `ByDifficulty[Hard] == AgentBucket{PassAtK: []float64{0, 0, 0}}`, `PerBug` ids `[x y]`.
- `TestSummarizeAgentsErrorsReduceGraded` — bug `z`: solved, unsolved, error → `Attempts 3, Graded 2, Solved 1`, `Errors 1`, `PassAtK [0.5 1 0]`.
- `TestSummarizeAgentsOneAttemptPerBug` — results with `Attempt == 0`, one per bug, 2 of 4 solved → `MaxAttempts 1`, `PassAtK [0.5]`.
- `TestSummarizeAgentsMedians` — solved costs 100, 300 easy and 900 hard → all 300, easy 200, hard 900; unsolved ignored.

`runner_test.go` (with `newRunnerEnv`):
- `TestRunAllRepeat` — `Repeat 3`, `Now` fixed to one instant, one bug, agents `first`, `second` → 6 results in the order first a1,a2,a3, second a1,a2,a3; `Attempt` 1,2,3,1,2,3; all six run ids distinct and ending `-a1`/`-a2`/`-a3`.
- `TestRunOneIsAttemptOne` — `RunOne` → `Attempt 1`, run id does not match `-a[0-9]+$`.

`report_test.go`:
- `TestPrintTablePassAtKLines` — one agent `coder@1`, bug `x` with 2 attempts, 1 solved → output contains `coder@1  attempts 2  pass@1 0.500  pass@2 1.000\n`.
- `TestOldReportLoads` — unmarshal a JSON literal of a pre-Q.1 report (no `attempt`, `repeat` or `by_agent`) → no error; `Summarize(rep.Results).ByAgent["coder@1"].MaxAttempts == 1`.

`compare_test.go`:
- `TestAgentName` — `coder@2`→`coder`, `a@b@3`→`a@b`, `plain`→`plain`.
- `TestCompareMatchesByName` — base `coder@1`, `other@1`; new `coder@2` → one pair, `BaseOnlyAgents [other@1]`.
- `TestCompareDuplicateName` — base has `coder@1` and `coder@2` → the exact error text above.
- `TestCompareNoCommonName` / `TestCompareMapUnknownAgent` — exact error texts.
- `TestCompareMap` — names differ, mapping `{a@1, b@1}` → one pair with those ids.
- `TestCompareCommonBugsOnly` — base bugs x, y; new bugs y, z → rows count only y; `BaseOnlyBugs [x]`, `NewOnlyBugs [z]`.
- `TestCompareStatusChanges` — x: 3/3 → 1/3, y: 1/3 → 3/3, w: 2/3 → 3/3 → changes x, y only, statuses as expected; a bug with only errors on one side → status `error`.
- `TestCompareKNeedsRepeatsOnBothSides` — base 1 attempt, new 3 → `K 0`; 3 and 3 → `K 3`; 2 and 3 → `K 2`.
- `TestPrintComparison` — build the `Comparison` of the full example as a literal (PassAtK values from the table; `Solved 0` for the new hard bucket) and compare the output to the example exactly.
- `TestPrintComparisonNoChangesNoRepeats` — K 0 → no `PASS@` header; output ends with `status changes: none\n`.

`eval_compare_test.go` (reports written with `evals.WriteReport` in `t.TempDir()`):
- `TestEvalCompareText` — code 0; stdout contains `coder@1 -> coder@2  bugs 2` and `status changes:`.
- `TestEvalCompareJSON` — `--json` → stdout unmarshals into `evals.Comparison` with one pair.
- `TestEvalCompareMapFlag` — differing names: without `--map` code 1, stderr contains `no agent name appears in both reports`; with `--map a@1=b@1` code 0; `--map a@1` → stderr contains `want BASE_AGENT=NEW_AGENT`.
- `TestEvalCompareMissingFile` — code 1, stderr contains `read report`.

`eval_test.go`:
- `TestEvalDryRunWorstCase` — now expects `bugs=1 agents=2 repeat=1 worst_case_usd=2.500000\n`.
- `TestEvalDryRunRepeat` — `--repeat 3` → `bugs=1 agents=2 repeat=3 worst_case_usd=7.500000\n`.
- `TestEvalRepeatOutOfRange` — `--repeat 0` and `--repeat 21` → code 1, stderr contains `eval: --repeat must be between 1 and 20`.

## Verify

```bash
make ci
```
Expected: passes.

```bash
go test ./internal/evals/ -run 'TestPassAtK|TestSummarizeAgents|TestRunAllRepeat|TestCompare|TestPrintComparison|TestOldReportLoads' -v
go test ./internal/cli/ -run 'TestEvalCompare|TestEvalDryRun|TestEvalRepeat' -v
```
Expected: every test PASS.

```bash
make build
ADLC_ANTHROPIC_MODEL=test-model ADLC_ANTHROPIC_PRICE_INPUT=1 ADLC_ANTHROPIC_PRICE_OUTPUT=1 \
ADLC_ANTHROPIC_PRICE_CACHE_READ=1 ADLC_ANTHROPIC_PRICE_CACHE_WRITE=1 \
  ./bin/adlc eval evals/suites/go-bugs --agents agents/sdlc/coder.anthropic.yaml --repeat 3 --dry-run
```
Expected (10 bugs, `max_usd: "2.00"`, no model is called): `bugs=10 agents=1 repeat=3 worst_case_usd=60.000000`.

## Out of scope

- Running attempts in parallel.
- Changing the per-result table rows (no attempt column) or `ProviderSummary`.
- Significance tests or confidence intervals.
- Failure causes in the comparison (task Q.2).
- Using the comparison as a promotion gate (task C.4).

## Pitfalls

- `new` is a Go builtin: name the parameter `newRep`, not `new`.
- Compute pass@k per bug, then average over bugs. `Solved / Graded` over all attempts is not pass@1 when bugs have different numbers of graded attempts.
- Do not use `math/big` or factorials; `C(n, k)` overflows quickly. Use the product in the algorithm.
- An error result is an attempt but not a graded one: it counts in `Attempts`, not in `Graded`, and never in pass@k.
- Compare from the results, never from the stored `summary`: old reports have no `by_agent`.
- `%+.3f` already prints the sign; money and steps deltas build the sign by hand.
- With a fixed test clock, run ids of repeats collide without the `-a<n>` suffix; that is what `TestRunAllRepeat` checks.
- Keep `RunOne`'s signature: the 1.8/1.9 tests call it.

## Done checklist

- [ ] `--repeat` works; results carry `attempt`; run ids are distinct; the dry run multiplies by N.
- [ ] `Summary.ByAgent` with `PassAtK`, `PerBug`; old reports load and summarize as one attempt.
- [ ] `adlc eval-compare` prints the exact format above and `--json`; matching by name and `--map` as specified.
- [ ] All tests listed exist and pass; each was seen failing once.
- [ ] `interfaces.md` §9 has the Q.1 line.
- [ ] `make ci` passes.
