# W.4 — Review loop (producer and critic)

Size: M · Depends on: W.3 · Branch: `task/W.4-review-loop` · PR title: `W.4: review loop with a critic agent and VERDICT lines`

## Goal

After this task, a task with `review:` (m3.md decision 9) runs as a loop of
**rounds**. In each round the producer (the task's own agent) runs until its
checks pass (up to `attempts` tries in that round, as in W.3); then the
**critic** (`review.critic`) runs in a fresh sandbox on the committed
workspace with a prompt that contains the task and the producer's whole
diff. The critic's final message ends with `VERDICT: APPROVE` or
`VERDICT: CHANGES`:

- `APPROVE` → `task_finished{role CRITIC, PASSED}`: the task is done.
- `CHANGES` → `task_finished{role CRITIC, CHANGES_REQUESTED}`, and the
  producer runs again in the next round with the critic's message added to
  its prompt.
- After `max_rounds` rounds without approval the workflow ends with
  `workflow_completed{REVIEW_EXHAUSTED}`.

The critic never changes the workspace: its sandbox is destroyed without
copying anything out. Everything is decided from the workflow log, so
`Resume` continues a review that was interrupted at any point.

This task replaces W.3's four review stubs; it does not change anything
else in the engine.

## Read first

- `AGENTS.md`; `docs/impl/m3.md` (decisions 3, 6, 8, 9; the event and API sections)
- `docs/impl/tasks/W.3-workflow-engine.md` — all of it, especially "Driving a
  workflow" (`next`, `action`, `runSpec`, `execRun`, `afterRun`,
  `parkedAction`, `finishTask`), "History scans", "Prompts", the decision on
  `ERROR`, and "Hooks for W.4 and W.5"
- `internal/workflow/engine.go`, `history.go`, `sources.go`, `budget.go`, `helpers_test.go` (as merged by W.3)
- `docs/impl/tasks/W.2-workflow-events.md`: how `Fold` sets `TaskState.Passed`, `Rounds` and `Commit` for critic events

## Files

Create:

```
internal/workflow/review.go         verdict, parseVerdict, criticPrompt, capText, the four hook
                                    implementations (producerRound, reviewNote, nextReviewAction,
                                    finishCritic), criticRun, constants
internal/workflow/review_test.go
```

Change:

```
internal/workflow/engine.go         delete the four review stubs (producerRound, reviewNote,
                                    nextReviewAction, finishCritic) from the hook block; nothing else
internal/workflow/helpers_test.go   fakeRunner: steps keyed by stepKey(tr) (critic runs use "<task>/critic")
```

No shared files (`go.mod`, `root.go`, `interfaces.md`, …) change. No proto
change: `TASK_ROLE_CRITIC`, `TASK_OUTCOME_CHANGES_REQUESTED` and
`WORKFLOW_OUTCOME_REVIEW_EXHAUSTED` exist since W.2.

## Design

### Rounds and attempts (decision)

| Counter | Meaning | Limit |
|---------|---------|-------|
| `round` | review round, 1-based on every run of a task with `review:` (0 without review, as in W.3) | `Review.MaxRounds` critic runs that said CHANGES |
| `attempt` | producer attempt number, **per task** (m3.md: "1-based, per task"): it keeps counting across rounds and never resets | `Task.Attempts` producer runs that failed (`CHECKS_FAILED` or `RUN_FAILED`) **in the same round** |
| `try` | restart of the same run after `ERROR` (W.3) | none |

- Round `r` of the next producer run = `1 + <number of CRITIC records with outcome CHANGES_REQUESTED>`.
- Inside a round, W.3's rules apply unchanged: `CHECKS_FAILED`/`RUN_FAILED`
  → next attempt in the same round with the retry note, until
  `p.failedInRound(r) >= t.Attempts` → `workflow_completed{FAILED}` (the
  checks failed; this is not a review outcome).
- A producer `PASSED` in round `r` → the critic runs for round `r`, with
  `attempt` = that producer's attempt.
- Critic `CHANGES_REQUESTED` in round `r`: `r >= MaxRounds` →
  `workflow_completed{REVIEW_EXHAUSTED}`; else producer attempt
  `lastProducer.attempt + 1`, round `r + 1`.

**Why check failures count per round.** The fix a reviewer asks for often
breaks a test first. With one shared counter, a task with `attempts: 2` that
needed two tries in round 1 would have no try left in round 2, so the
reviewer's request would end the workflow as `FAILED` on a single red test.
Counting per round keeps `attempts` meaning "tries to get the checks green
for one version of the change" in every round. The worst case is bounded:
at most `MaxRounds × Attempts` producer runs and `MaxRounds` critic runs
(plus `ERROR` restarts), and the workflow budget caps the cost anyway.

Example, `attempts: 2`, `max_rounds: 2`:

```
P a1 r1 CHECKS_FAILED → P a2 r1 PASSED → C a2 r1 CHANGES_REQUESTED
→ P a3 r2 CHECKS_FAILED → P a4 r2 PASSED → C a4 r2 PASSED → task done
```

### Run ids and commits

From W.3 (unchanged): producer `….<task>.a<attempt>.r<round>`, critic
`….<task>.a<attempt>.r<round>.critic`, plus `.t<try>` after an `ERROR`.
Producer commits are `task <id> attempt <n> round <r>` (W.3's
`commitMessage`). Critic runs make **no commit**: `task_finished.commit` is
`""` for every critic record.

### Verdict (`review.go`)

```go
// verdict is a critic's decision.
type verdict int

const (
	verdictNone    verdict = iota // no valid VERDICT line
	verdictApprove
	verdictChanges
)

// verdictRE matches a verdict line after trimming.
var verdictRE = regexp.MustCompile(`(?i)^verdict\s*:\s*(approve|changes)\s*\.?$`)

// parseVerdict reads the verdict from the critic's final message. line is the
// verdict line as matched (trimmed), body the message without it (trimmed).
func parseVerdict(msg string) (v verdict, line, body string)
```

Exact rule:

1. `lines := strings.Split(msg, "\n")`. Find the **last** index `i` with
   `strings.TrimSpace(lines[i]) != ""`. None → `verdictNone, "", ""`.
2. `cand := strings.TrimSpace(strings.Trim(strings.TrimSpace(lines[i]), "*_`"))`
   — models often wrap the line in Markdown bold or code; `\r` from CRLF is
   removed by `TrimSpace`.
3. `m := verdictRE.FindStringSubmatch(cand)`. No match → `verdictNone, "", strings.TrimSpace(msg)`.
4. `strings.EqualFold(m[1], "approve")` → `verdictApprove`, else `verdictChanges`;
   `line = cand`; `body = strings.TrimSpace(strings.Join(lines[:i], "\n"))`.

So: case does not matter (`Verdict: approve`), spaces around `:` and one
trailing `.` are allowed, Markdown `*`, `_` and `` ` `` around the line are
ignored; anything else on the line (`VERDICT: APPROVE, nice work`) or any
non-empty line after it means **no verdict**.

**Decision: a missing or invalid verdict counts as `CHANGES`.** The
workflow must never pass a task on a review that did not explicitly approve
it (a critic that ran out of output or wrote prose only has not approved
anything). Treating it as `CHANGES` still uses what the critic wrote as
feedback, and `max_rounds` bounds the loop. Treating it as an error would
stop the workflow on a formatting slip; treating it as approval would turn
every malformed review into a pass. The detail says what happened:

```go
// noVerdictDetail is the task_finished detail of a critic message without a valid verdict line.
const noVerdictDetail = "no verdict line; treated as VERDICT: CHANGES"
```

### Critic prompt (`review.go`)

```go
// criticPromptFormat is the critic's prompt. Arguments: the rendered task
// prompt, the producer's diff, the checks text.
const criticPromptFormat = "You are reviewing a change that another agent made to the repository in /workspace.\n" +
	"\n" +
	"## Task\n" +
	"\n" +
	"The change was made for this task:\n" +
	"\n" +
	"%s\n" +
	"\n" +
	"## Change\n" +
	"\n" +
	"This is the whole change of the task so far, as git diff against the workspace before the task:\n" +
	"\n" +
	"```diff\n" +
	"%s\n" +
	"```\n" +
	"\n" +
	"## Checks\n" +
	"\n" +
	"%s\n" +
	"\n" +
	"## Your review\n" +
	"\n" +
	"Read the change and, where you need more context, the files in /workspace. " +
	"Do not edit any file: your changes are discarded. " +
	"Decide whether the change does what the task asks, correctly and completely, " +
	"with tests where the task needs them, and without unrelated edits.\n" +
	"\n" +
	"Write your review as a list of the problems you found, each with the file and what must change. " +
	"If there are none, say so. End your reply with one last line that is exactly one of:\n" +
	"\n" +
	"VERDICT: APPROVE\n" +
	"VERDICT: CHANGES\n" +
	"\n" +
	"Write VERDICT: APPROVE only if the change needs no more work.\n"

// checksPassedHeader starts the checks text when the task has checks.
const checksPassedHeader = "These acceptance checks passed on this change:"

// noChecksText is the checks text of a task without checks.
const noChecksText = "This task has no acceptance checks."

// noChangesText replaces an empty diff.
const noChangesText = "(no changes)"

// criticPrompt builds the critic's prompt.
func criticPrompt(taskPrompt, diff string, checks []string) string
```

`criticPrompt` = `fmt.Sprintf(criticPromptFormat, strings.TrimRight(taskPrompt, "\n"), d, c)` where

- `d` = `strings.TrimRight(capText(diff), "\n")`, or `noChangesText` when `diff == ""`;
- `c` = `checksPassedHeader + "\n- " + strings.Join(checks, "\n- ")`, or `noChecksText` when there are none.

```go
// capText returns s unchanged if it fits in MaxTemplateValueBytes, else its
// first MaxTemplateValueBytes bytes (cut back to a whole UTF-8 rune) followed by TruncatedMarker.
func capText(s string) string
```

Inputs of `criticPrompt` for the critic of round `r`:

- `taskPrompt` = `Render(t, sources)` now (the workspace is the producer's
  commit), **without** review or retry notes: the critic judges the change
  against the task, not against earlier feedback.
- `diff` = `f.git.diff(p.BaseCommit, prod.Finished.Commit)` with
  `prod := p.lastOf(PRODUCER)` (its outcome is `PASSED`): the whole change of
  the task so far, so the critic of round 2 sees the full result, not only
  the fix.
- `checks` = `t.Checks`.

A render or diff error → `actComplete{FAILED, "task <id>: render prompt: <err>"}`
(W.3's rule for render errors).

### Producer feedback (`review.go`)

```go
// reviewNoteFormat is appended to a producer prompt in round > 1. Arguments:
// the round of the critic that asked for changes, its feedback.
const reviewNoteFormat = "\n\n## Review feedback (round %d)\n\n" +
	"A reviewer read your change and asked for changes. The workspace contains your change as the reviewer saw it. " +
	"Address every point, keep what is already right, and make sure every check still passes. The reviewer wrote:\n\n" +
	"%s\n"

// noFeedbackText replaces an empty critic message.
const noFeedbackText = "(the reviewer gave no details)"
```

**`reviewNote(ctx, t, p, round)`** (replaces the W.3 stub):

1. `t.Review == nil || round <= 1` → `"", nil`.
2. `c :=` the last CRITIC record with `Round == round-1` and outcome
   `CHANGES_REQUESTED` (none → error `workflow: task %s: no review for round %d`).
3. `st, err := e.o.Runs.State(ctx, c.Started.RunId)`; `_, _, body := parseVerdict(st.Result)`.
4. `body == ""` → `noFeedbackText`; else `capText(body)`.
5. `fmt.Sprintf(reviewNoteFormat, round-1, body)`.

W.3 builds the producer prompt as `Render` + `reviewNote` + retry note, so a
retry inside round 2 still carries the feedback of round 1 plus the check
output of its own previous attempt. The verdict line itself is not passed on.

### The four hooks (`review.go`)

```go
// producerRound is the review round of t's next producer run: 0 without review,
// else 1 + the number of critic runs that asked for changes.
func (e *Engine) producerRound(t Task, p taskProgress) uint32

func (e *Engine) reviewNote(ctx context.Context, t Task, p taskProgress, round uint32) (string, error)

// nextReviewAction decides the next step of a task with review when the last
// record is a producer PASSED of round > 0 or any critic record.
func (e *Engine) nextReviewAction(ctx context.Context, f *flow, st *State, events []*adlcv1.Event, t Task, p taskProgress) (action, error)

// finishCritic finishes a critic run: it parses the verdict and writes task_finished. It never collects the workspace.
func (e *Engine) finishCritic(ctx context.Context, f *flow, t Task, rec *runRecord, h sandbox.Handle, state *eventlog.RunState, runErr error) error

// criticRun builds the critic's runSpec for round of the producer attempt, or actComplete{FAILED} on a render error.
func (e *Engine) criticRun(ctx context.Context, f *flow, st *State, events []*adlcv1.Event, t Task, p taskProgress, attempt, round, try uint32) (action, error)
```

`criticRun` returns `actRun` with
`runSpec{Role: CRITIC, AgentRef: t.Review.Critic, Attempt: attempt, Round: round, Try: try, Prompt: criticPrompt(...)}`.
W.3's `execRun` does the rest unchanged: the budget is
`nestBudget(AgentBudget(t.Review.Critic), t.Budget, w.Budget, spent)`
(**decision: the task's `budget:` limits the critic's run too** — it is a run
of the task — and the critic's cost counts towards the workflow budget),
`task_started{role CRITIC, attempt, round, run_id ….critic, budget}`, a fresh
sandbox from the workspace (which is the producer's commit), `RunTask` with
`Role: CRITIC`, then `afterRun` → `finishCritic`.

**`nextReviewAction`, exact rules** (`last := p.last()`):

| `last` | Action |
|--------|--------|
| `t.Review == nil` | error `workflow: task %s: review action without review` |
| PRODUCER, `PASSED`, round `r > 0` | `criticRun(attempt = last.attempt, round = r, try = 1)` |
| CRITIC, `ERROR` | `criticRun(same attempt, same round, try = 1 + p.tries(CRITIC, attempt, round))` |
| CRITIC, `AWAITING_APPROVAL` | `e.parkedAction(ctx, t, last)` (W.3: park again, or continue → `finishCritic`) |
| CRITIC, `RUN_FAILED` | `actComplete{FAILED, fmt.Sprintf("task %s: critic run %s failed: %s", t.ID, last.run_id, firstLine(last.detail))}` |
| CRITIC, `CHANGES_REQUESTED`, round `r >= MaxRounds` | `actComplete{REVIEW_EXHAUSTED, fmt.Sprintf("task %s: not approved after %d review round(s)", t.ID, r)}` |
| CRITIC, `CHANGES_REQUESTED`, round `r < MaxRounds` | `e.producerRun(ctx, f, st, events, t, p, p.lastOf(PRODUCER).attempt + 1, r + 1, 1)` |
| CRITIC, `PASSED` | error `workflow: task %s: critic approved but the task is not passed` (the fold should have marked it passed) |
| anything else | error `workflow: task %s: unexpected outcome %s` |

**Decision: a critic run that does not complete fails the workflow.** A
critic that stops with `BUDGET_EXHAUSTED`, `DENIED` or `ERROR` has given no
review; re-running it with the same agent and budget would most likely stop
the same way (and loop until the workflow budget runs out), and counting it
as `CHANGES` would make the producer redo work for no reason. `FAILED` with
the critic's stop detail tells the person what to fix (usually the critic's
budget). Infrastructure failures (`ERROR`) are still restarted on `Resume`,
as in W.3.

**`finishCritic`**, exact order (`keep := false; defer: if !keep, destroy h`;
every `finishTask` has `commit == ""`):

| # | Condition | outcome, detail | Returns |
|---|-----------|-----------------|---------|
| 1 | `ctx.Err() != nil` | `ERROR`, `"cancelled"` | nil |
| 2 | `runErr != nil` | `ERROR`, `"run task: " + runErr` | `workflow: task %s: run task: %w` |
| 3 | `state == nil` | `ERROR`, `"run task returned no state"` | `workflow: task %s: run task returned no state` |
| 4 | `AWAITING_APPROVAL` | `AWAITING_APPROVAL`, `"sandbox " + h.ID`; `keep = true` | nil |
| 5 | not `COMPLETED` | `RUN_FAILED`, `runStoppedDetail(state)` | nil |
| 6 | `parseVerdict(state.Result)` = approve | `PASSED`, `line` (e.g. `VERDICT: APPROVE`) | nil |
| 7 | = changes | `CHANGES_REQUESTED`, `line` | nil |
| 8 | = none | `CHANGES_REQUESTED`, `noVerdictDetail` | nil |

No `CopyOut`, no commit, no checks: whatever the critic changed in its
sandbox is destroyed with it.

### Events written

| Situation | Events (workflow log) |
|-----------|-----------------------|
| producer round `r` | `task_started{PRODUCER, attempt, round r, run_id ….a<n>.r<r>}`, `task_finished{PRODUCER, …, commit}` (W.3) |
| critic round `r` | `task_started{CRITIC, attempt of the reviewed producer run, round r, run_id ….a<n>.r<r>.critic, budget}`, `task_finished{CRITIC, PASSED / CHANGES_REQUESTED / RUN_FAILED / AWAITING_APPROVAL / ERROR, detail, commit ""}` |
| reviews used up | `workflow_completed{REVIEW_EXHAUSTED, "task <id>: not approved after <n> review round(s)"}` |
| critic failed | `workflow_completed{FAILED, "task <id>: critic run <run-id> failed: <detail>"}` |

What W.4 relies on from W.2's `Fold` (check it in step 1): `TaskState.Passed`
becomes true on a CRITIC `PASSED` and **not** on a PRODUCER `PASSED` with
`round > 0`; `TaskState.Commit` ignores `commit == ""`; `Rounds` is the
highest round started. `Sources.Result(task)` (W.3) already reads the last
**producer** `PASSED` run, and `Sources.Diff(task)` the last non-empty commit,
so later tasks see the producer's work, never the critic's message.

### Resume in the middle of a review

All decisions come from the log (W.3's `next` + `nextReviewAction`), so:

| Last event | Resume does |
|------------|-------------|
| `task_finished{PRODUCER, PASSED, round r}` | starts the critic of round `r` |
| `task_started{CRITIC}` (crash during the critic) | W.3 closes it `ERROR`/interrupted; critic restarts with try + 1 (`….critic.t2`), same attempt and round; the producer is not re-run |
| `task_finished{CRITIC, CHANGES_REQUESTED, r}` | producer round `r + 1` with the feedback note (read from the critic's child run in `Runs`) — or `REVIEW_EXHAUSTED` when `r == MaxRounds` |
| `task_finished{CRITIC, PASSED}` | next task |
| `task_finished{CRITIC, AWAITING_APPROVAL}` | parks again, or continues the critic once its run finished (no collect) |
| `task_finished{CRITIC, RUN_FAILED}` | `workflow_completed{FAILED}` |

W.3's Resume restores the workspace to the last recorded commit; critics
record none, so the critic's sandbox always starts from the commit it reviews.

## Steps

1. Branch from the latest `main` (W.3 merged). Read `engine.go`'s hook block
   and check that the four stub signatures match this brief. Check the
   `Fold` behaviour listed under "Events written". If anything differs, stop
   and report.
2. Write `parseVerdict`, `capText`, `criticPrompt` and their unit tests.
3. Delete the four stubs from `engine.go`; implement them and `criticRun` in `review.go`.
4. `helpers_test.go`: `stepKey(tr)` = `tr.Task.ID` for producers,
   `tr.Task.ID + "/critic"` for critics; the fake uses it to pick its step queue.
5. Write the engine-level tests in `review_test.go`.
6. Run every W.3 test unchanged: they must still pass (tasks without
   `review:` keep round 0).
7. `make fmt && make ci`. Break each rule once (e.g. count attempts across
   rounds, accept a verdict that is not the last line) and see a test fail.

## Tests

`review_test.go` (helpers from W.3's `helpers_test.go`; skip without git).
A review workflow in tests:

```yaml
name: t
version: 1
tasks:
  - id: impl
    agent: producer.yaml
    prompt: "Implement it."
    checks: ["test -f done.txt"]
    attempts: 2
    review: { critic: critic.yaml, max_rounds: 2 }
  - id: after
    agent: producer.yaml
    prompt: "{{ result \"impl\" }}|{{ diff \"impl\" }}"
```

| Test | Asserts |
|------|---------|
| `TestParseVerdict` | table (msg → verdict, line, body): `"ok\nVERDICT: APPROVE"` → approve, `VERDICT: APPROVE`, `ok`; `"x\nverdict: changes"` → changes; `"x\n  **VERDICT: APPROVE**  \n\n"` → approve, `VERDICT: APPROVE`; `"x\n`VERDICT: CHANGES`"` → changes; `"x\nVerdict : Approve."` → approve; `"x\r\nVERDICT: APPROVE\r\n"` → approve, body `x`; `"VERDICT: APPROVE\nthanks"` → none, body = whole message; `"VERDICT: APPROVE, nice"` → none; `"VERDICT: MAYBE"` → none; `""` → none, `""`, `""` |
| `TestCriticPromptExact` | `criticPrompt("Implement it.\n", "diff --git a/x b/x\n+y\n", []string{"go build ./...", "go test ./..."})` equals the full expected string written out in the test; with no checks the checks section is `noChecksText`; with `""` diff it is `(no changes)` |
| `TestCapText` | `MaxTemplateValueBytes + 10` bytes → length `MaxTemplateValueBytes + len(TruncatedMarker)` at most, ends with `TruncatedMarker`, valid UTF-8 (input with a 3-byte rune across the cut); short text unchanged |
| `TestReviewApprovedFirstRound` | producer step creates `done.txt`; critic step result `"Looks right.\nVERDICT: APPROVE"` → kinds `workflow_started, task_started, task_finished, task_started, task_finished, …` with: P `attempt 1 round 1 PASSED` run id `….impl.a1.r1`, commit message `task impl attempt 1 round 1`; C `attempt 1 round 1 PASSED` detail `VERDICT: APPROVE`, run id `….impl.a1.r1.critic`, commit `""`; then task `after` runs; `COMPLETED`; `TaskState["impl"].Passed` |
| `TestCriticTaskRun` | the critic's `TaskRun`: `Role == CRITIC`, `AgentRef == "critic.yaml"`, `Prompt == criticPrompt(rendered, diff of done.txt, checks)`; `AgentBudget` was called with `critic.yaml`; `task_started.budget` is the nested critic budget |
| `TestCriticChangesAreDiscarded` | critic step writes `critic.txt` and edits `done.txt` → neither change is in the workspace nor in any commit; the critic's sandbox is destroyed |
| `TestReviewChangesThenApprove` | critic 1 `"Add a test.\nVERDICT: CHANGES"`, critic 2 approves → producer 2 is `attempt 2 round 2`, run id `….impl.a2.r2`, prompt == `"Implement it."` + `fmt.Sprintf(reviewNoteFormat, 1, "Add a test.")` (whole string); critic 2 is `….impl.a2.r2.critic` and its prompt's diff is the cumulative change from the task's base commit; `COMPLETED` |
| `TestReviewExhausted` | critic says CHANGES twice → `workflow_completed{REVIEW_EXHAUSTED, "task impl: not approved after 2 review round(s)"}`; 2 producer and 2 critic runs; task `after` never starts |
| `TestMissingVerdictCountsAsChanges` | critic 1 result `"Please rename x."` (no verdict) → critic `task_finished{CHANGES_REQUESTED, noVerdictDetail}`; producer 2's feedback is `Please rename x.` |
| `TestEmptyCriticMessage` | critic 1 result `"VERDICT: CHANGES"` → producer 2's feedback is `noFeedbackText` |
| `TestChecksFailInsideReviewRounds` | the example under "Rounds and attempts": run ids `….a1.r1`, `….a2.r1`, `….a2.r1.critic`, `….a3.r2`, `….a4.r2`, `….a4.r2.critic`; outcomes CHECKS_FAILED, PASSED, CHANGES_REQUESTED, CHECKS_FAILED, PASSED, PASSED; producer a4's prompt = rendered + feedback note (round 1) + retry note of a3; `COMPLETED` |
| `TestChecksExhaustedInsideRound2` | round 2 fails checks twice → `workflow_completed{FAILED, "task impl failed after 2 attempt(s): …"}` (not REVIEW_EXHAUSTED) |
| `TestCriticRunFailedFailsWorkflow` | critic step `stop: BUDGET_EXHAUSTED` → critic `RUN_FAILED`; `workflow_completed{FAILED}` with detail prefix `task impl: critic run ….impl.a1.r1.critic failed: run stopped: BUDGET_EXHAUSTED` |
| `TestCriticRunTaskErrorIsRestarted` | critic step `err` → `Start` returns the error, last event critic `ERROR`; `Resume` → critic run id `….impl.a1.r1.critic.t2`, producer not re-run |
| `TestCriticParksAndContinues` | critic step `stop: AWAITING_APPROVAL` → workflow `awaiting_approval`, critic sandbox kept; finish the critic's child run with result `"ok\nVERDICT: APPROVE"` (append `approval_resolved` and `run_completed` to `Runs`) → `Resume` writes critic `task_started` (same run id) + `task_finished{PASSED}`; no commit is made; sandbox destroyed |
| `TestResumeDuringCriticRun` | `truncateLog` after the critic's `task_started` → `Resume` appends critic `ERROR` (interrupted), critic `….critic.t2`, its `task_finished`; the producer fake is not called again |
| `TestResumeAfterChangesRequested` | cut after critic 1's `task_finished{CHANGES_REQUESTED}` → `Resume` runs producer `attempt 2 round 2` with the feedback note read from critic 1's child run |
| `TestResumeAfterProducerPassedBeforeCritic` | cut after producer 1's `task_finished{PASSED}` → `Resume` runs only the critic of round 1 |
| `TestResultAndDiffUseProducer` | in the two-round approve run, task `after`'s prompt is `<producer 2's result>|<diff from impl's base to producer 2's commit>` — never the critic's message |
| `TestNoReviewKeepsRoundZero` | a task without `review:` → `round 0`, no critic run, run id without `.r` (W.3 behaviour unchanged) |

## Verify

```bash
go test ./internal/workflow/ -run 'Verdict|CriticPrompt|CapText' -v           # PASS
go test ./internal/workflow/ -run 'Review|Critic|Missing|Empty|ChecksFail|ChecksExhausted|ResumeDuring|ResumeAfter|ResultAndDiff|NoReview' -v
go test -race ./internal/workflow/                                            # every W.3 test still PASS
make ci                                                                       # passes
```

## Out of scope

- A per-task critic prompt template in the workflow file (the `Review` type
  has no such field; the default text is the only one).
- Several critics, critics that edit the workspace, voting.
- Gates and export (task W.5); the CLI (task W.5); SDLC agents (task W.6).
- Any change to the proto, `Fold`, or W.3's rules for tasks without review.

## Pitfalls

- The verdict is the **last non-empty line**; do not search the whole
  message for `VERDICT:` (a critic quoting the instructions would approve itself).
- Never treat a missing verdict as approval.
- The critic's prompt is rendered without the producer's notes; the
  producer's prompt carries the feedback. Keep them apart.
- `attempt` never resets; the attempts *limit* counts per round
  (`p.failedInRound(r)`, already in W.3). Do not add a second counter.
- Critic records always have `commit == ""`; never call `collect` for a critic.
- Destroy the critic's sandbox on every path except `AWAITING_APPROVAL`.
- The feedback comes from the critic's child run in `Options.Runs`
  (`RunState.Result`), not from `task_finished.detail` (that holds only the
  verdict line).
- Remove the W.3 stubs; leaving them and adding new functions with the same
  names does not compile — that is the point.

## Done checklist

- [ ] `review.go` with `parseVerdict`, `criticPrompt`, `capText`, the four hooks and `criticRun`; the stubs removed from `engine.go`
- [ ] Verdict rule exactly as specified; missing verdict = CHANGES with `noVerdictDetail`
- [ ] Critic runs in a fresh sandbox, never commits, sandbox destroyed (kept only when parked)
- [ ] Producer rounds with the exact feedback note; attempts limited per round, numbered per task
- [ ] `REVIEW_EXHAUSTED` after `max_rounds`; critic failure → `FAILED`
- [ ] Resume works after each event of a review (tests above)
- [ ] All W.3 tests pass unchanged; every new test was seen failing once
- [ ] `make ci` passes
