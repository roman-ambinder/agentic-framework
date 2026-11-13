# Q.3 — Fork a run from a checkpoint with a different agent: `adlc fork`

Size: M · Depends on: — · Branch: `task/Q.3-fork-run` · PR title: `Q.3: adlc fork, continue a run's transcript with another agent`

## Goal

After this task `adlc fork RUN_ID --at-step N AGENT_YAML [--run-id NEW]`
creates a new run whose log starts with a copy of RUN_ID's events up to and
including the checkpoint of step N, then continues from step N+1 with the
agent in AGENT_YAML (which may use another model, prompt, effort or budget).
This lets you try a change from the same point without paying for, or
re-rolling, the first N steps. The new run's `run_started` records where it
came from. The new log is self-contained: `adlc trace`, `adlc cost` and
`adlc resume` work on it like on any run.

**Limitation, stated plainly:** only the conversation is forked. The sandbox
workspace at step N is **not** restored: the code has no sandbox snapshot
(`sandbox.Sandbox` has no `Snapshot`, and checkpoints have an empty
`snapshot_ref`). A fork whose agent has a `sandbox:` section gets a fresh
sandbox from that section, like `adlc run`; whatever the origin run changed in
its workspace before step N is missing, although the transcript says it
happened. The command prints a warning saying so.

## Read first

- `AGENTS.md` (rules 4 and 5: generated code and additive proto changes), `docs/impl/m2.md` (decision 5)
- `docs/impl/interfaces.md` §6 (harness, checkpoints, `Resume`), §6.1, §10
- `docs/impl/tasks/1.7-resume.md` — how `Resume` reuses the loop
- `internal/harness/resume.go` — `loadForResume`, `verifyLastCheckpoint`, `newResumeLoop`, `resume`
- `internal/harness/loop.go` — `newLoop`, `run`, `steps`, `startWallClock`, `loadTools`; `internal/harness/hooks.go` — `writeCheckpoint`, `VerifyCheckpoints`
- `internal/harness/recorder.go` — `add` sets `RunId` and `AgentId`; `modelCalls`
- `internal/harness/transcript.go` — `Transcript` (rebuilds every request from the run's own events)
- `internal/eventlog/fold.go` — `RunState`, `Digest`; `internal/eventlog/store.go` — `Append` (fills `Seq`, and `Time` only when unset)
- `internal/cli/run.go`, `internal/cli/run_sandbox.go`, `internal/cli/resume.go`

## Files

Change:
- `contracts/proto/adlc/v1/events.proto` — two fields in `RunStarted`; then `make proto` (regenerates `gen/go`; never edit `gen/` by hand)
- `internal/cli/root.go` — add `root.AddCommand(newForkCmd())` (one added line)
- `docs/impl/interfaces.md` — §6 and §10 additions below, only if not already there

Create:
- `internal/harness/fork.go`, `internal/harness/fork_test.go`
- `internal/cli/fork.go`, `internal/cli/fork_test.go`

## Design

### Schema (additive)

`RunStarted` already has `resumed_from_run_id = 6`, but nothing writes it
(`Resume` continues a run in place) and a fork is not a resume, so it gets
its own fields:

```proto
message RunStarted {
  ...
  string resumed_from_run_id = 6;
  // Run this one was forked from (adlc fork), if any.
  string forked_from_run_id = 7;
  // Step of forked_from_run_id whose checkpoint the fork copied up to.
  uint32 forked_at_step = 8;
}
```

Go names after `make proto`: `ForkedFromRunId`, `ForkedAtStep`. **Do not add
them to `eventlog.RunState`**: `RunState.Digest` hashes every field, so a new
field would change the digest of every existing checkpoint and break `Resume`
of old runs (see the comment on `RunResumed` in `fold.go`). Read the origin
from the `run_started` event when you need it.

### Harness API (`fork.go`)

```go
// ForkPoint names the run and the step a fork starts from.
type ForkPoint struct {
	RunID  string // origin run
	AtStep uint32 // >= 1; the origin must have a checkpoint for this step
}

var (
	// ErrNoCheckpoint means the origin run has no checkpoint for the step.
	ErrNoCheckpoint = errors.New("harness: no checkpoint for that step")
	// ErrRunExists means the fork's run id is already in the log.
	ErrRunExists = errors.New("harness: run already exists")
)

// ForkEvents returns the events a fork copies: the origin's events from seq 1
// up to and including its first checkpoint with Step == atStep.
func ForkEvents(events []*adlcv1.Event, atStep uint32) ([]*adlcv1.Event, error)

// Fork starts the run cfg.RunID from the transcript of from.RunID up to and
// including step from.AtStep and continues it with cfg and deps. cfg.Task is
// ignored. Like Run, the error is non-nil only for failures to use the log
// and for refusing the fork; every other failure ends the run with a stop reason.
func Fork(ctx context.Context, cfg Config, deps Deps, from ForkPoint) (*eventlog.RunState, error)
```

`ForkEvents` algorithm:
1. `atStep == 0` → `errors.New("harness: fork: step must be at least 1")`.
2. No events → `fmt.Errorf("%w: no events", eventlog.ErrRunNotFound)`.
3. Find the first index `i` with `events[i].GetCheckpoint() != nil && events[i].GetCheckpoint().GetStep() == atStep`. None → `fmt.Errorf("%w: run %q, step %d (checkpointed steps: %s)", ErrNoCheckpoint, events[0].GetRunId(), atStep, list)`, `list` = the distinct checkpoint steps in log order joined with `", "`, or `none`.
4. `prefix := events[:i+1]`; `state, err := eventlog.Fold(prefix)` (error → `fmt.Errorf("harness: fork: %w", err)`).
5. `verifyLastCheckpoint(prefix, state)` — `state.LastCheckpoint` is that checkpoint; a mismatch returns its `ErrCheckpointMismatch` error.
6. Return `prefix`.

`Fork` algorithm:
1. `cfg, deps = applyDefaults(cfg, deps)`; `validate(cfg, deps)` (no task check); `cfg.Context = effectiveBudget(cfg.Context, deps.Model.Capabilities())`, as `Resume` does.
2. `origin, err := deps.Log.Read(ctx, from.RunID, 0)`; error → `fmt.Errorf("harness: fork: read log: %w", err)`; empty → `fmt.Errorf("%w: %q", eventlog.ErrRunNotFound, from.RunID)`.
3. `prefix, err := ForkEvents(origin, from.AtStep)`.
4. `existing, err := deps.Log.Read(ctx, cfg.RunID, 0)`; non-empty → `fmt.Errorf("%w: %q", ErrRunExists, cfg.RunID)` (this also refuses forking a run into itself). Nothing has been written yet.
5. `l := newLoop(ctx, cfg, deps)`; `return l.fork(prefix, from)`.

`(l *loop) fork(prefix, from)`:
1. `l.startedAt = l.deps.Now()`; `cancel := l.startWallClock()`; `defer cancel()`.
2. `l.add` a `run_started`: `AgentVersion: cfg.AgentVersion`, `Task: prefix[0].GetRunStarted().GetTask()`, `Budget: cfg.Budget`, `ForkedFromRunId: from.RunID`, `ForkedAtStep: from.AtStep`.
3. For each `ev` in `prefix[1:]`, in order:
   - a checkpoint → `l.writeCheckpoint(ev.GetCheckpoint().GetStep())` (a new digest of the fork's own state; the origin's digest covers another run id and other times, so it cannot be copied);
   - anything else → `c := proto.CloneOf(ev)`; `c.Seq, c.Time, c.Cost = 0, nil, nil`; `l.add(c)` (it sets the fork's `RunId` and `AgentId`; `ParentSeq` stays, it is still valid because seqs line up).
   - Then if `l.r.state.LastSeq != ev.GetSeq()` → `fmt.Errorf("harness: fork: copied seq %d stored as %d", ev.GetSeq(), l.r.state.LastSeq)`.
4. `l.loadTools()`; `return l.steps(uint32(l.r.modelCalls) + 1)` (with the usual `//nolint:gosec` comment). For a fork at step N this is step N+1, call id `m<N+1>`.

Consequences (write them in the doc comment of `Fork` too):
- **Cost:** copied events carry no `Cost`. The money was spent once, by the origin; `adlc cost` would count it twice otherwise. The fork's `Usage` and its `max_usd` / `max_tokens` budget count only its own calls.
- **Counted limits:** `max_steps` and `max_tool_calls` count the copied steps and tool calls too (they are in the transcript); the wall clock starts at the fork.
- **Times:** copied events get the time they are copied at (`Time` cleared), so `StartedAt <= LastEventAt` holds. Latencies and durations inside payloads are kept.
- **Model change:** copied `provider_blobs` are replayed by `Transcript`; adapters drop blobs of other providers (interfaces.md §2), so switching provider is safe.
- **Tools:** copied tool calls name the origin's tools; if the new agent lacks a tool, the model only sees it in history. Tool outputs are re-capped with the new agent's `max_tool_output_bytes`.

### `adlc fork` (`internal/cli/fork.go`)

```go
// forkOptions are the flags of `adlc fork`.
type forkOptions struct {
	runOptions        // agentPath, db, fakeScript, runID, modelName, workspaceOut, keepSandbox, startHook
	atStep     uint32
}
```

`Use: "fork RUN_ID AGENT_YAML --at-step N"`, `Short: "Start a new run from a run's transcript up to a step, with another agent"`, `cobra.ExactArgs(2)`
(`args[0]` origin, `args[1]` → `o.agentPath`), signal context as in `adlc run`.
Flags: `--at-step` (`f.Uint32Var`, required via `MarkFlagRequired`), `--run-id`,
`--db` (default `defaultDBPath`), `--fake-script`, `--model-name`,
`--workspace-out`, `--keep-sandbox` — same help texts as `adlc run`.

`forkAgentWith(ctx, o forkOptions, originID string, stdout io.Writer, openSandbox openRunBackend) error`,
every failure `&exitError{code: 1, err: …}`; the command passes `openDockerRunBackend`:
1. `o.atStep == 0` → `fork: --at-step must be at least 1`.
2. `a, err := loadAgent(o.runOptions)`.
3. Run id: `o.runID`, or `newRunID(time.Now(), rand.Reader)`; an invalid one → `fork: invalid --run-id %q`.
4. `store, err := openExistingLog(ctx, o.db)`; `defer store.Close()`.
5. Checks **before any sandbox or tool starts**: `origin := store.Read(ctx, originID, 0)`; empty → `fmt.Errorf("fork: %w: %q", eventlog.ErrRunNotFound, originID)`; `prefix, err := harness.ForkEvents(origin, o.atStep)`; `store.Read(ctx, runID, 0)` non-empty → `fmt.Errorf("fork: %w: %q", harness.ErrRunExists, runID)`.
6. `sb, err := startRunSandbox(ctx, a, runID, openSandbox)`; `defer sb.close()`. If `sb != nil`, print `fmt.Sprintf("warning: the sandbox starts from the agent file's workspace; workspace changes of run %s up to step %d are not restored\n", originID, o.atStep)`.
7. `cfg, deps, cleanup, err := buildRun(ctx, a, o.runOptions, runID, store)`; on error `sb.destroy(ctx, stdout)`; else `defer cleanup()`.
8. `state, err := harness.Fork(ctx, cfg, deps, harness.ForkPoint{RunID: originID, AtStep: o.atStep})`; on error `sb.keepForResume(stdout)` and return.
9. Print `fmt.Sprintf("%-9s%s at step %d (%d events copied)\n", "forked:", originID, o.atStep, len(prefix)-1)`, then `printSummary(stdout, state)`, then `sb.finish(ctx, o.runOptions, state, stdout)`.
10. Exit 0 for `COMPLETED`, else `&exitError{code: 2}` (as `adlc run`).

Example (origin `o1` stopped after 6 steps; fork from step 4 with another effort):

```
$ adlc fork o1 --at-step 4 agents/coder-medium.yaml --run-id o1-medium
forked:  o1 at step 4 (31 events copied)
run:     o1-medium
agent:   coder-medium@1
stop:    completed
steps:   7  tool calls: 12  errors: 0
tokens:  in 41250  out 2210  cache read 0  cache write 0
cost:    $0.156300
result:  Fixed the rounding in money.Split and added money_issue_test.go.
```

`steps` and `tool calls` include the copied part; `tokens` and `cost` do not.

**Eval runs:** the sandbox tools of `adlc eval` come from the eval runner, not
from the agent file, so forking an eval run with an eval agent file gives the
fork no sandbox and no tools. That is a known limitation of this task.

### Interfaces

Add to `interfaces.md` §6, after the `Resume` block:

```go
// Fork starts cfg.RunID from the transcript of from.RunID up to and including
// the checkpoint of step from.AtStep, then continues it with cfg (task Q.3).
type ForkPoint struct {
	RunID  string
	AtStep uint32
}
func Fork(ctx context.Context, cfg Config, deps Deps, from ForkPoint) (*eventlog.RunState, error)
func ForkEvents(events []*adlcv1.Event, atStep uint32) ([]*adlcv1.Event, error)
var (
	ErrNoCheckpoint = errors.New("harness: no checkpoint for that step")
	ErrRunExists    = errors.New("harness: run already exists")
)
```

and to the §10 table:

```
| Q.3 | `RunStarted` | `string forked_from_run_id` | 7 |
| Q.3 | `RunStarted` | `uint32 forked_at_step` | 8 |
```

## Steps

1. Branch from the latest `main`. Check §10 and `events.proto`: numbers 7 and 8 of `RunStarted` must be free. If not, stop and report.
2. Add the two fields, `make proto`, `make proto-lint`, `make proto-breaking` (passes: additions only).
3. `fork.go` with `ForkEvents` and its tests first, then `Fork` and `loop.fork` with their tests.
4. `internal/cli/fork.go`, register it in `root.go`, CLI tests.
5. Update `interfaces.md`.
6. `make fmt`, then Verify.

## Tests

`internal/harness/fork_test.go`. `origin(t)` runs `baseConfig()` (run `r1`) on
a new store with turns `toolTurn(readCall("t1","a"))`, `toolTurn(readCall("t2","b"))`,
`textTurn("done")`: 16 events, checkpoints at seq 7 (step 1) and 13 (step 2).
`forkCfg` = `baseConfig()` with `RunID "f1"`, `AgentID`/`AgentVersion "other@2"`.
- `TestForkEventsReturnsPrefix` — step 1 → 7 events ending with the step-1 checkpoint; step 2 → 13.
- `TestForkEventsNoCheckpoint` — step 3 → `errors.Is(err, ErrNoCheckpoint)` and the message contains `checkpointed steps: 1, 2`; a log without checkpoints → `checkpointed steps: none`.
- `TestForkEventsStepZero` — error text exactly `harness: fork: step must be at least 1`.
- `TestForkContinuesFromStep` — fork at step 1 with `textTurn("forked")` → stop `COMPLETED`, `Result "forked"`; the model's first request equals `Transcript(prefix, 0)` (3 messages: task, the `t1` call, its result `contents of a`); `run_started` has `ForkedFromRunId "r1"`, `ForkedAtStep 1`, `Task` of the origin, `AgentVersion "other@2"`; kinds of fork events 2..7 equal the origin's; the next `model_call_started` has `Step 2`, `CallId "m2"`; `assertFolds` passes; `VerifyCheckpoints(fork events) == nil`.
- `TestForkIgnoresTask` — `forkCfg.Task = "other"` → `run_started.Task` is still the origin's.
- `TestForkLeavesOriginUnchanged` — the origin's events before and after are `proto.Equal`, same count.
- `TestForkUnknownRun` — `errors.Is(err, eventlog.ErrRunNotFound)`; nothing written for `f1`.
- `TestForkRunIDExists` — `forkCfg.RunID = "r1"` → `errors.Is(err, ErrRunExists)`; `r1` still has 16 events.
- `TestForkCheckpointMismatch` — a `Log` wrapper whose `Read("r1")` returns the events with one `tool_call_completed` before the checkpoint flipped to `IsError: true` → `errors.Is(err, ErrCheckpointMismatch)`.
- `TestForkDoesNotCopyCost` — origin turns with `Usage{InputTokens: 1_000_000}` and `Price{InputPerMTok: 1_000_000}`; fork at step 2 with one text turn of the same usage → every copied event has `Cost == nil`; fork `Usage.USDMicros == 1_000_000`.
- `TestForkStepBudgetCountsCopiedSteps` — fork at step 2 with `Budget{MaxSteps: 2}` → `BUDGET_EXHAUSTED` (steps) and the fake model was never called (`Remaining() == 1`).
- `TestForkedRunCanBeResumed` — fork through a `failingLog` that stops right after the new `model_call_started` (prefix length + 1 appends); `Resume` with `forkCfg` and a new fake → `COMPLETED`.

`internal/cli/fork_test.go` (origin: `run hello.yaml --task … --run-id o1` with `helloDir`;
fork agent: `helloYAML` with `name: hello2`, written next to it; fork script
`{"turns": [{"text": "Forked answer", "finish": "end_turn", "usage": {"input_tokens": 5, "output_tokens": 2}}]}`):
- `TestForkCommandContinuesFromStep` — `fork o1 hello2.yaml --at-step 1 --run-id f1 --db … --fake-script …` → code 0; stdout contains `forked:  o1 at step 1 (6 events copied)\n`, `run:     f1\n`, `agent:   hello2@1\n`, `steps:   2  tool calls: 1  errors: 0\n`, `result:  Forked answer\n`; in the store, `f1`'s `run_started` has `ForkedFromRunId "o1"`, `ForkedAtStep 1`.
- `TestForkCommandErrors` — table, each code 1 and nothing written for the new id (`store.Runs` is `[o1]`): unknown origin → `run not found`; `--at-step 2` → `no checkpoint for that step`; `--run-id o1` → `run already exists`; `--at-step 0` → `fork: --at-step must be at least 1`; no `--at-step` → `required flag(s) "at-step" not set`; `--run-id "bad id"` → `fork: invalid --run-id`.
- `TestForkSandboxIsFresh` — fork agent = `hello2` plus the `sandbox:` section of `sandboxRun`; `forkAgentWith` with a `newRecordingBackend` → one `Create` with `RunID "f1"`; stdout contains the warning line exactly and `workspace: `.
- `TestForkChecksBeforeCreatingSandbox` — same sandbox agent, `--at-step 2` → error; the open function calls `t.Fatal` if used.

## Verify

```bash
make ci
make proto-breaking
```
Expected: both pass.

```bash
go test ./internal/harness/ -run 'TestFork' -v
go test ./internal/cli/ -run 'TestForkCommand|TestForkSandbox|TestForkChecks' -v
```
Expected: every test PASS.

```bash
make build
./bin/adlc run examples/hello.yaml --task "What does greeting.txt say?" --fake-script examples/hello.fake.json --run-id demo-o1 --db /tmp/fork-demo.db
./bin/adlc fork demo-o1 --at-step 1 examples/hello.yaml --fake-script examples/hello.fake.json --run-id demo-f1 --db /tmp/fork-demo.db
./bin/adlc trace demo-f1 --db /tmp/fork-demo.db
```
Expected: the fork prints `forked:  demo-o1 at step 1 (6 events copied)`. The fake script starts again from its first turn, so step 2 reads the file once more and step 3 answers: `stop:    completed`, `steps:   3  tool calls: 2  errors: 0`, exit 0. The trace shows three steps, the first one copied. Delete `/tmp/fork-demo.db` afterwards (`rm /tmp/fork-demo.db`).

## Out of scope

- Restoring the workspace at step N (needs `Sandbox.Snapshot`, planned in the design doc, not in the code).
- Forking from a point that is not a checkpoint (mid-step), or from a paused run's pending approval.
- Forking eval runs inside `adlc eval`, and running several forks at once.
- Showing the fork origin in `adlc trace` output.
- Adding the fork fields to `RunState`.

## Pitfalls

- Never copy a checkpoint's `state_digest`: it is the digest of the origin's state (other run id, other times). Write a new checkpoint with `writeCheckpoint`.
- Clear `Time` on copies: `Store.Append` keeps a set time, and old times before the new `run_started` make the fork's `LastEventAt` earlier than its `StartedAt`.
- Clear `Seq` on copies (or set it to the origin's seq): a stale non-zero seq that does not match makes `Append` fail with `ErrSeqConflict`.
- Do the CLI checks (step 5) before `startRunSandbox`, so a typo in the step does not create a container.
- `recorder.add` overwrites `AgentId`: copied events are attributed to the fork's agent. That is intended; the origin is in `run_started`.
- Do not add `ForkedFromRunID` to `RunState`; it would change every digest.
- `make proto` must run after the `.proto` edit; CI's `proto-check` fails on stale `gen/`.
- If task Q.4 (plan step) is merged first: `Fork` never runs a plan call (it does not go through `run()`); a plan the origin made is copied like any other event, and `recorder.modelCalls` already skips it, so `modelCalls + 1` is still step N+1.

## Done checklist

- [ ] `RunStarted.forked_from_run_id = 7`, `forked_at_step = 8` added; `gen/go` regenerated; `make proto-breaking` passes.
- [ ] `harness.Fork`, `ForkEvents`, `ForkPoint`, `ErrNoCheckpoint`, `ErrRunExists` exist as specified.
- [ ] `adlc fork` works, checks before creating a sandbox, and warns that the workspace is not restored.
- [ ] All tests listed exist and pass; each was seen failing once.
- [ ] `interfaces.md` §6 and §10 updated.
- [ ] `make ci` passes.
