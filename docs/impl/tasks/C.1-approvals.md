# C.1 — Approvals: `adlc approvals`, `adlc approve`, continue a paused run

Size: M · Depends on: — · Branch: `task/C.1-approvals` · PR title: `C.1: list approvals and continue a paused run with adlc approve`

## Goal

Today a tool call whose policy decision is `NEEDS_APPROVAL` parks the run:
the log gets `approval_requested{approval_id: "a-<call id>"}` and then
`run_completed{AWAITING_APPROVAL}`, and nothing can continue it
(`harness.Resume` refuses with `ErrRunPaused`). After this task:

- `adlc approvals` lists every pending approval of every paused run in the log;
- `adlc approve APPROVAL_ID RUN_ID [AGENT_YAML]` appends `approval_resolved`
  to the same run (m2.md decision 1) and, when no approval is left pending,
  continues the run in place: an approved call runs through the hooks and the
  tool router (the policy is **not** asked again), a rejected call gets an
  error tool result the model sees, then the normal loop goes on.

No schema change: `ApprovalResolved{approval_id, approved, resolved_by, comment}`
already has every field this task needs. No new dependencies.

## Read first

- `AGENTS.md`; `docs/impl/m2.md`; `docs/impl/interfaces.md` §3, §6
- `docs/impl/tasks/1.7-resume.md` (how a run is continued; this task copies its shape)
- `internal/eventlog/fold.go` (a paused run accepts only `approval_resolved`; it sets `Status` back to running; a rejection adds 1 to `Denials`)
- `internal/harness/loop.go` (`runToolCall`, `authorize`: the approval id and summary), `resume.go` (`newResumeLoop`, `verifyLastCheckpoint`, wall clock), `hooks.go`, `recorder.go`, `transcript.go`
- `internal/harness/helpers_test.go`, `resume_test.go` (`needsApproval`, `crashRun`), `hooks_test.go` (`testHook`)
- `internal/cli/resume.go` (`attachSandbox`, `openSandboxBackend`), `wiring.go` (`buildRun`), `run.go` (`loadAgent`, `runOptions`), `summary.go`, `openlog.go`, `root.go`, `resume_test.go` (`crashedRun`), `run_test.go` (`helloDir`, `helloYAML`)

## Files

Create:

```
internal/harness/approve.go
internal/harness/approve_test.go
internal/cli/approve.go          both commands: approvals and approve
internal/cli/approve_test.go
```

Change:

```
internal/harness/loop.go         extract execute() from runToolCall (no behaviour change)
internal/cli/root.go             two AddCommand lines
internal/cli/cli_test.go         add "approvals" and "approve" to TestCommandsShareDefaultDB
docs/impl/interfaces.md          §6: the additions below
```

Do not change `events.proto`, `fold.go`, `transcript.go` or `resume.go`.

## Design

### Harness API (`internal/harness/approve.go`)

```go
var (
	// ErrRunNotPaused means the run is not waiting for an approval.
	ErrRunNotPaused = errors.New("harness: run is not waiting for approval")
	// ErrUnknownApproval means the run has no approval_requested with this id.
	ErrUnknownApproval = errors.New("harness: unknown approval")
	// ErrApprovalResolved means the approval already has an approval_resolved.
	ErrApprovalResolved = errors.New("harness: approval already resolved")
	// ErrContinueRun means ResolveApproval was asked to resolve the last
	// pending approval; that must go through Approve, which continues the run.
	ErrContinueRun = errors.New("harness: last pending approval: the run must be continued with Approve")
)

// Resolution is a person's answer to one pending approval.
type Resolution struct {
	ApprovalID string
	Approved   bool
	By         string // required: who resolved it
	Comment    string // optional
}

// CheckApproval folds a run's events and checks that approvalID can be
// resolved now. It returns the folded state. Checks, in this order:
// no events → eventlog.ErrRunNotFound; no approval_requested with that id →
// ErrUnknownApproval; an approval_resolved with that id → ErrApprovalResolved;
// status not paused → ErrRunNotPaused.
func CheckApproval(events []*adlcv1.Event, runID, approvalID string) (*eventlog.RunState, error)

// ResolveApproval records res for a run that will still wait for other
// approvals afterwards: it appends approval_resolved and then
// run_completed{AWAITING_APPROVAL}. If res resolves the last pending approval
// it appends nothing and returns ErrContinueRun.
func ResolveApproval(ctx context.Context, log Log, runID string, res Resolution) (*eventlog.RunState, error)

// Approve records res and, if no approval is left pending, continues the run
// in place. cfg.Task is ignored. Errors as CheckApproval, plus
// ErrAgentMismatch and ErrCheckpointMismatch exactly as Resume reports them.
func Approve(ctx context.Context, cfg Config, deps Deps, res Resolution) (*eventlog.RunState, error)

// RejectedOutput is the tool result of a rejected call:
// "rejected by <by>" or, with a comment, "rejected by <by>: <comment>".
func RejectedOutput(by, comment string) string
```

Exact error texts (tests check them with `errors.Is` and `strings.Contains`):

| Case | Error |
|------|-------|
| no events | `fmt.Errorf("%w: %q", eventlog.ErrRunNotFound, runID)` |
| fold fails | `fmt.Errorf("harness: approve: %w", err)` |
| unknown id | `fmt.Errorf("%w: %q in run %q", ErrUnknownApproval, approvalID, runID)` |
| already resolved | `fmt.Errorf("%w: %q", ErrApprovalResolved, approvalID)` |
| not paused | `fmt.Errorf("%w: run %q is %s", ErrRunNotPaused, runID, state.Status)` (Status prints `running` / `finished`) |
| `res.By == ""` | `errors.New("harness: approve: resolved by is empty")` (checked first, before reading the log) |
| bad paused log (`planApproval`, below) | `fmt.Errorf("harness: approve: tool call %q was requested but has no result and no approval", id)` |
| no `model_call_completed` before the approval | `errors.New("harness: approve: no model response before the approval")` |

### Several pending approvals

Today's loop pauses at the first `NEEDS_APPROVAL`, so a paused run normally has
exactly one pending approval. Logs with several are still handled:
resolve **one at a time**; while others remain, the run stays paused (the
resolution is recorded, nothing runs); the run continues only when the last
one is resolved, and then every call of the step is handled with its own
recorded resolution.

Why `run_completed{AWAITING_APPROVAL}` again: the fold sets a paused run back
to running on any `approval_resolved`; appending the pause again keeps the log
honest (the fold accepts it because approvals are still pending).

### `ResolveApproval` algorithm

1. `res.By == ""` → error. `events := log.Read(ctx, runID, 0)`.
2. `state, err := CheckApproval(events, runID, res.ApprovalID)`; error → return it.
3. `len(state.PendingApprovals) == 1` → return `nil, ErrContinueRun`.
4. `r := newRecorderFrom(ctx, log, runID, state.AgentID, events, *state)`; append
   `approval_resolved{approval_id, approved, resolved_by: res.By, comment}` with
   `ParentSeq` = seq of the matching `approval_requested`; then append
   `run_completed{stop_reason: AWAITING_APPROVAL}`. Wrap append errors as
   `fmt.Errorf("harness: %w", err)`.
5. Return a copy of `r.state` (status paused).

### `Approve` algorithm

```
Approve(ctx, cfg, deps, res):
 1. res.By == "" → error. cfg, deps = applyDefaults(cfg, deps); validate(cfg, deps);
    cfg.Context = effectiveBudget(cfg.Context, deps.Model.Capabilities())   (same as Resume)
 2. events := deps.Log.Read(ctx, cfg.RunID, 0); state, err := CheckApproval(events, cfg.RunID, res.ApprovalID)
 3. cfg.AgentVersion != state.AgentVersion → same error as Resume (ErrAgentMismatch, same format)
 4. verifyLastCheckpoint(events, state) (from resume.go)
 5. len(state.PendingApprovals) > 1 → return ResolveApproval(ctx, deps.Log, cfg.RunID, res)
 6. plan, err := planApproval(events, res)           // before anything is appended
 7. l := newResumeLoop(ctx, cfg, deps, events, *state)
    used := state.LastEventAt.Sub(state.StartedAt); l.startedAt = deps.Now().Add(-used)
    cancel := l.startWallClock(); defer cancel(); l.loadTools()
 8. append approval_resolved (fields as in ResolveApproval, ParentSeq = its approval_requested seq)
 9. for each item of plan, in order:   (stop and return l.final, err when err != nil or l.final != nil)
      done      → nothing
      approved  → l.runApproved(item.call, item.reqSeq)
      rejected  → append tool_call_completed{call_id, is_error: true,
                   output: RejectedOutput(item.by, item.comment)} with ParentSeq = item.reqSeq
      notRun    → l.runToolCall(item.call)              (the normal path, policy included)
10. l.writeCheckpoint(l.r.state.Step)
11. return l.steps(uint32(l.r.modelCalls) + 1)
```

Wall clock: the time a run spends paused does not count against
`max_wall_clock`; `used` is the time up to the pause (`LastEventAt` before the
`approval_resolved` is appended).

`planApproval(events, res)` returns `[]approvalItem{kind, call model.ToolCall, reqSeq uint64, by, comment string}`
(`by` and `comment` come from that item's own resolution):

1. `mc, tail := lastModelCompleted(events)` (exists in resume.go); `mc == nil` → error.
2. For each `tc` in `mc.GetToolCalls()`, in order, build
   `call := model.ToolCall{ID: tc.GetId(), Name: tc.GetName(), Arguments: json.RawMessage(tc.GetArgumentsJson())}`
   (empty arguments → `json.RawMessage("{}")`), then look at `tail`:
   - a `tool_call_completed` with `call_id == tc.Id` → kind `done`;
   - else an `approval_requested` with `call_id == tc.Id` → its approval id `aid`;
     resolution = `res` if `aid == res.ApprovalID`, else the `approval_resolved`
     with `approval_id == aid` in `tail` (it exists: no other approval is pending);
     kind `approved` or `rejected`; `reqSeq` = seq of the call's `tool_call_requested`;
   - else no `tool_call_requested` for `tc.Id` → kind `notRun`;
   - else (requested, no result, no approval) → error (table above).

Match approvals to calls through `approval_requested.call_id`, never by
cutting `"a-"` off the approval id.

`runApproved(call, reqSeq)` in `approve.go`:

1. `l.ctx.Err() != nil` → `l.finish(CANCELLED, "", "")`.
2. `tool, known := l.byName[call.Name]`; not known → append the same error
   `tool_call_completed` that `runToolCall` writes for an unknown tool
   (`l.unknownToolMessage`), `ParentSeq = reqSeq`, and return.
3. `stopped, err := l.runPreHooks(call, tool, reqSeq)`; stopped or err → return
   (a hook may still deny: the run ends DENIED as in a normal step).
4. `return l.execute(call, reqSeq)`. **No** `l.authorize`, **no** budget check
   (the call was counted when it was requested).

### Refactor in `loop.go`

Move the tail of `runToolCall` (from `res, err := l.callTool(call)` to the end)
into:

```go
// execute runs an authorized call, records tool_call_completed and runs the
// PostTool hooks. It sets l.final when the run stops.
func (l *loop) execute(call model.ToolCall, reqSeq uint64) error
```

`runToolCall` ends with `return l.execute(call, reqSeq)`. All existing harness
tests must pass unchanged.

### Event sequence (example: one call needing approval, then a final answer)

```
... model_call_completed{tool_calls: [w1]}  tool_call_requested w1  policy_decided NEEDS_APPROVAL
    approval_requested{a-w1, w1, "x__write {...}"}  run_completed AWAITING_APPROVAL
--- adlc approve a-w1 r1 agent.yaml ---
approval_resolved{a-w1, approved: true, resolved_by: "bob"}   parent_seq = seq of approval_requested
tool_call_completed{w1, output}                                parent_seq = seq of tool_call_requested w1
checkpoint
model_call_started  model_call_completed  run_completed COMPLETED
```

Rejected: the `tool_call_completed` has `is_error: true, output: "rejected by bob: not now"`.
`Transcript` needs no change: it already ignores approval and `run_completed` events.

### CLI (`internal/cli/approve.go`)

```
adlc approvals [--db PATH]
adlc approve APPROVAL_ID RUN_ID [AGENT_YAML] [--reject] [--comment TEXT] [--by NAME]
             [--db PATH] [--fake-script PATH] [--model-name NAME]
```

`newApprovalsCmd()` (`cobra.NoArgs`) and `newApproveCmd()` (`cobra.RangeArgs(2, 3)`);
`--db` default `defaultDBPath` for both. In `root.go` add
`root.AddCommand(newApprovalsCmd())` and `root.AddCommand(newApproveCmd())` after
`newResumeCmd()`.

**`adlc approvals`:** `openExistingLog`; `ids := store.Runs(ctx)`; for each id:
`store.Read`, `eventlog.Fold` (error → exit 1, `approvals: run %q: %w`); skip
runs whose status is not paused; for each id in `state.PendingApprovals` (in
order) find its `approval_requested` event and print one row. Output through
`tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)`, every line written with
`"%s\t%s\t%s\t%s\t%s\t%s\n"` (no trailing tab):

```
APPROVAL  RUN                          CALL    AGENT    REQUESTED             SUMMARY
a-call_1  r1                           call_1  hello@1  2026-11-13T10:00:00Z  fs__write_file {"path":"out.txt","content":"hi"}
a-m1-t1   run-20261113T100500Z-0a1b2c  m1-t1   coder@1  2026-11-13T10:05:07Z  shell__run {"cmd":"make deploy"}
```

REQUESTED = the event time, `ev.GetTime().AsTime().UTC().Format(time.RFC3339)`.
SUMMARY = `approval_requested.summary` with `\n`, `\r`, `\t` replaced by a
space; if it is longer than 80 runes, its first 77 runes plus `...`.
No pending approval at all → print exactly `no pending approvals\n`. Exit 0.

**`adlc approve`:**

1. `by` = `--by`; if empty, `os.Getenv("USER")`; if empty, `"local"`. Put it in
   `func approverName(flag string, getenv func(string) string) string`.
2. `store := openExistingLog(ctx, o.db)`; `events := store.Read(ctx, runID, 0)`;
   `st, err := harness.CheckApproval(events, runID, approvalID)` → exit 1 on error.
3. `res := harness.Resolution{ApprovalID, Approved: !reject, By: by, Comment: comment}`.
4. `len(st.PendingApprovals) > 1` → `harness.ResolveApproval(ctx, store, runID, res)`;
   print `approval <id> <approved|rejected>; run <run id> still waits for <n> approval(s)\n`
   (n = pending count after this one); exit 0. AGENT_YAML is not loaded.
5. Otherwise AGENT_YAML is required: missing → exit 1 with
   `approve: AGENT_YAML is required to continue run "<run id>"` and nothing appended.
6. `loadAgent(runOptions{agentPath, db, fakeScript, modelName})`;
   `attachSandbox(ctx, a, runID, openSandboxBackend)`; `buildRun(ctx, a, o, runID, store)`;
   `defer cleanup()`; `harness.Approve(ctx, cfg, deps, res)` — same order and
   error handling as `resumeAgent` (exit 1 on any error). Use
   `signal.NotifyContext` like `resume`.
7. `printSummary(stdout, state)`; exit 0 if `STOP_REASON_COMPLETED`, else exit 2
   (paused again, denied, …), exactly like `adlc run`.

Write `approveRun(ctx, o approveOptions, approvalID, runID string, stdout io.Writer, openSandbox func(context.Context) (sandboxLookup, error)) error`
so tests can call it; the cobra command passes `openSandboxBackend`.

### `interfaces.md` §6 addition

Append after the `Resume` block (exact text):

```go
// Approvals (task C.1). Approve continues a paused run in place once its last
// pending approval is resolved; ResolveApproval records one of several.
type Resolution struct {
	ApprovalID string
	Approved   bool
	By         string // required
	Comment    string
}
func CheckApproval(events []*adlcv1.Event, runID, approvalID string) (*eventlog.RunState, error)
func ResolveApproval(ctx context.Context, log Log, runID string, res Resolution) (*eventlog.RunState, error)
func Approve(ctx context.Context, cfg Config, deps Deps, res Resolution) (*eventlog.RunState, error)
func RejectedOutput(by, comment string) string // "rejected by <by>[: <comment>]"
var (
	ErrRunNotPaused     = errors.New("harness: run is not waiting for approval")
	ErrUnknownApproval  = errors.New("harness: unknown approval")
	ErrApprovalResolved = errors.New("harness: approval already resolved")
	ErrContinueRun      = errors.New("harness: last pending approval: the run must be continued with Approve")
)
```

and this paragraph under it: "`Approve` appends `approval_resolved` (parent =
its `approval_requested`); an approved call runs PreTool hooks, the tool and
PostTool hooks without a second policy check; a rejected call gets an error
`tool_call_completed` with `RejectedOutput`; the remaining calls of the step
run normally, then a checkpoint, then the loop. Time spent paused does not
count against `max_wall_clock`."

## Steps

1. Branch from latest `main` (AGENTS.md).
2. Extract `execute` in `loop.go`; `go test ./internal/harness/` passes unchanged.
3. `approve.go`: errors, `Resolution`, `RejectedOutput`, `CheckApproval`, `ResolveApproval`, `planApproval`, `runApproved`, `Approve`.
4. `approve_test.go` (harness); see each new test fail once by breaking the code.
5. `internal/cli/approve.go`, `root.go`, `cli_test.go`; `approve_test.go` (cli).
6. `interfaces.md` §6 additions (below).
7. `make fmt && make ci`.

## Tests

`internal/harness/approve_test.go`. Helpers: `writeTool(calls *int)` = `funcTool("x__write", WRITE_LOCAL, fn)`
where fn counts calls and returns `"wrote " + args`; `writeCall(id)` =
`model.ToolCall{ID: id, Name: "x__write", Arguments: {"path":"p"}}`; policy =
`policy.NewStatic([]policy.Rule{{ID: "writes", Risk: WRITE_LOCAL, Decision: NEEDS_APPROVAL}}, ALLOW)`
wrapped in a `countingPolicy` that counts `Authorize` calls. `pausedRun(t, turns...)`
runs `Run` on `newLog(t)` and asserts status paused. Every test that continues a
run checks with `assertFolds` and that the events before the call are unchanged
(compare with `proto.Equal`, as `resumeOK` does).

| Test | Asserts |
|------|---------|
| `TestApproveRunsWaitingCall` | paused at `a-w1`; `Approve(…, Resolution{"a-w1", true, "bob", ""})` with fake `[textTurn("done")]` → tool called once; appended kinds start `approval_resolved, tool_call_completed, checkpoint, model_call_started`; `resolved_by == "bob"`, its `parent_seq` = seq of `approval_requested`; final COMPLETED "done"; the model's first request ends with the result of `w1` |
| `TestApproveDoesNotAskPolicyAgain` | `countingPolicy` count is 1 after Run and still 1 after Approve; the log has exactly one `policy_decided` |
| `TestApproveRunsPreToolHooks` | `testHook` sees `x__write` in PreTool twice (before the pause and on approve) and in PostTool once |
| `TestApproveHookDenies` | a hook that denies only on its second call → run ends DENIED `rule hook:<name>: <reason>`; tool never called |
| `TestApproveRejectedCallNeverReachesTool` | `Approved: false, Comment: "not now"` → tool call count 0; `tool_call_completed{is_error: true, output: "rejected by bob: not now"}`; the model's first request holds that error result; run COMPLETED; `state.Denials == 1` |
| `TestRejectedOutput` | `("bob","")` → `rejected by bob`; `("bob","no")` → `rejected by bob: no` |
| `TestApproveRunsRestOfStep` | turn `[w1, readCall("r1","a")]`: after approve, `w1` runs, then `r1` gets `tool_call_requested`, `policy_decided` ALLOW, `tool_call_completed`, then one `checkpoint` |
| `TestApprovePausesAgain` | turn `[w1, w2]`: approve `a-w1` → `w2` is requested and the run pauses with pending `[a-w2]`; model not called; approve `a-w2` → COMPLETED, tool called twice |
| `TestApproveSeveralPending` | hand-built log (append to the store: `run_started`, `model_call_started{step 1}`, `model_call_completed{w1,w2}`, then for w1 and w2 each `tool_call_requested`, `policy_decided` NEEDS_APPROVAL, `approval_requested (ids a-w1 and a-w2)`, then `run_completed` AWAITING_APPROVAL; every event with `RunId: "r1"`, `AgentId: "test@1"`, `run_started.agent_version: "test@1"`, `parent_seq` 0); `Approve(a-w1)` → state paused, appended exactly `approval_resolved, run_completed`, fake not called, tool not called; `Approve(a-w2, rejected)` → `w1` runs, `w2` gets the rejected result, run COMPLETED |
| `TestResolveApprovalRefusesLast` | single pending → `ErrContinueRun`, log length unchanged |
| `TestApproveUnknownRun` | `errors.Is(err, eventlog.ErrRunNotFound)` |
| `TestApproveUnknownApproval` | id `a-nope` → `ErrUnknownApproval`; nothing appended |
| `TestApproveAlreadyResolved` | approve, then the same id again → `ErrApprovalResolved` |
| `TestApproveRunNotPaused` | `crashRun` until the `approval_requested` event (status running, approval pending) → `ErrRunNotPaused`, message contains `is running` |
| `TestApproveAgentMismatch` | other `AgentVersion` → `ErrAgentMismatch`; nothing appended |
| `TestApproveNeedsBy` | `By: ""` → error `resolved by is empty` |
| `TestApproveWallClockExcludesPause` | `stepClock` + `clockedLog(t, clock)` as in `TestResumeWallClockUsesTimeSpent` (the store must stamp events with the same clock); budget `max_wall_clock: 60s`; a READ tool `x__slow10` advances the clock 10s; turn `[x__slow10 call, w1]` pauses at `a-w1`; clock moves 1h; approve with `[textTurn("done")]` → COMPLETED, not BUDGET_EXHAUSTED |

`internal/cli/approve_test.go`. Agent: `helloYAML` with the policy rules
`reads` (read → allow) and `writes` (`risk: write_local, decision: needs_approval`).
Run script: one turn, text `Writing.`, tool call `{"id": "call_1", "name": "fs__write_file", "arguments": {"path":"out.txt","content":"hi"}}`
(arguments written compact, exactly so), finish `tool_use`. Approve script:
`{"turns": [{"text": "Done.", "finish": "end_turn", "usage": {"input_tokens": 1, "output_tokens": 1}}]}`.
`pausedRun(t)` makes run `r1` as `crashedRun` does, but with
`eventlog.Open(ctx, db, eventlog.WithClock(fixed))`, `fixed` = 2026-11-13T10:00:00Z, and a plain store.

| Test | Asserts |
|------|---------|
| `TestApprovalsListsPending` | exit 0 and stdout is exactly the two lines below (no trailing spaces) |

```
APPROVAL  RUN  CALL    AGENT    REQUESTED             SUMMARY
a-call_1  r1   call_1  hello@1  2026-11-13T10:00:00Z  fs__write_file {"path":"out.txt","content":"hi"}
```

| Test | Asserts |
|------|---------|
| `TestApprovalsNone` | empty log with one completed run → `no pending approvals\n` |
| `TestApproveCommandCompletesRun` | `approve a-call_1 r1 <agent> --fake-script <approve script> --db <db> --by bob` → exit 0, stdout contains `stop:    completed`, `workspace/out.txt` contains `hi`; `approvals` then prints `no pending approvals` |
| `TestApproveCommandRejectDoesNotWrite` | `--reject --comment no` → exit 0; `workspace/out.txt` does not exist; the log has `tool_call_completed` output `rejected by bob: no` |
| `TestApproveCommandNeedsAgentFile` | no AGENT_YAML → exit 1, stderr contains `AGENT_YAML is required to continue run "r1"`, event count unchanged |
| `TestApproveCommandUnknownApproval` | `approve a-x r1 …` → exit 1, stderr contains `unknown approval` |
| `TestApproverName` | flag wins; else `USER`; else `local` (pass a fake `getenv`) |

## Verify

```bash
go test -race ./internal/harness/ -run 'Approve|ResolveApproval|Rejected' -v   # all PASS
go test ./internal/harness/                                                    # existing tests unchanged, PASS
go test ./internal/cli/ -run 'Approv|CommandsShareDefaultDB' -v                # all PASS
make ci                                                                        # passes
```

## Out of scope

- Approvals from a server, Slack or a web UI (M4); notification of approvers.
- Expiry or timeouts of approvals; who is allowed to approve (no identity check: `--by` is recorded as given).
- Copying out or destroying the sandbox workspace after an approved run ends (as for `adlc resume`, the container stays).
- Changing `adlc resume` (it still refuses paused runs).
- Automatic approval by policy (`resolved_by: "policy:<rule>"`).

## Pitfalls

- Never call `l.authorize` for the approved call: the approval is the decision.
- Build the plan before appending anything, so a refused approve leaves the log untouched.
- `parent_seq` of `approval_resolved` is the `approval_requested` seq; of the result, the call's `tool_call_requested` seq.
- The fold already counts a rejection in `Denials`; do not write an extra `policy_decided` for it.
- The calls after the paused one were never requested: they go through `runToolCall` (with policy), and may pause the run again.
- Fake scripts start from their first turn in every process: the approve script holds only the turns after the pause.
- `TestCommandsShareDefaultDB` must include both new commands.
- Do not slice `"a-"` off approval ids; use `approval_requested.call_id`.

## Done checklist

- [ ] `execute` extracted; existing harness tests unchanged and passing
- [ ] `CheckApproval`, `ResolveApproval`, `Approve`, `RejectedOutput` and the four errors, with the exact texts
- [ ] A rejected call never reaches the tool (test)
- [ ] `adlc approvals` and `adlc approve` registered, with tests
- [ ] No change to `events.proto`, `fold.go`, `transcript.go`
- [ ] `interfaces.md` §6 updated
- [ ] `make ci` passes
