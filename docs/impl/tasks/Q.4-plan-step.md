# Q.4 — Plan-then-act step

Size: S · Depends on: — · Branch: `task/Q.4-plan-step` · PR title: `Q.4: optional planning call before the first step`

## Goal

An agent file can switch on a planning call (`planning.enabled: true`). It is
off by default (m2.md decision 6), so `adlc eval` can compare the same agent
with and without it. When it is on, the harness makes one extra model call
before step 1: no tools, and a prompt asking for a short plan. The plan is
recorded as a normal `model_call_started` / `model_call_completed` pair with
call id `plan` and step 0. `Transcript` puts it into the conversation, so every
later request contains it. The plan call counts towards the token and cost
budgets but not towards `max_steps`. `Resume` handles runs that have a plan.
When planning is off, the log and every request stay exactly as they are today.

## Read first

- `AGENTS.md`, `docs/impl/m2.md` (decisions 3, 5, 6), `docs/impl/interfaces.md` §6, §6.1, §7
- `internal/harness/harness.go`, `loop.go`, `recorder.go`, `transcript.go`, `resume.go`, `budget.go`
- `internal/harness/helpers_test.go`, `harness_test.go` (helpers `defaultDeps`, `wantKinds`, `wantStop`,
  `wantBudgetStop`, `countModelCalls`, `toolTurns`, `rateLimited`, `sleepRecorder`), `resume_test.go`
  (`crashRun`, `resumeOK`), `prefix_test.go` (`checkPrefix`)
- `internal/eventlog/fold.go` (`ModelCallStarted` sets `Step = max(Step, step)`: step 0 is accepted, nothing to change there)
- `internal/agentconfig/agentconfig.go`, `validate.go`; `internal/cli/wiring.go` (`harnessConfig`)

## Files

| File | Change |
|------|--------|
| `internal/harness/harness.go` | `PlanConfig`, `Config.Plan`, the four constants, defaults, validation |
| `internal/harness/plan.go` | new: `runPlan`, `planRequest`, `hasPlan` |
| `internal/harness/loop.go` | `run` calls `runPlan` before `steps(1)` |
| `internal/harness/recorder.go` | the plan call is not counted in `modelCalls` |
| `internal/harness/transcript.go` | plan → assistant message + fixed user message |
| `internal/harness/resume.go` | skip the plan in `lastModelCompleted`; redo a plan that never completed |
| `internal/harness/plan_test.go` | new |
| `internal/harness/transcript_test.go` | two tests added |
| `internal/agentconfig/agentconfig.go`, `validate.go`, `agentconfig_test.go` | `planning:` section |
| `internal/agentconfig/testdata/full.yaml` | documented `planning:` block |
| `internal/cli/wiring.go` | `harnessConfig` sets `cfg.Plan` |
| `internal/cli/wiring_plan_test.go` | new |
| `docs/impl/interfaces.md` | §6, §6.1, §7 (text below) |

No change to `events.proto`, `internal/eventlog`, `internal/contextmgr` or `internal/observability`.

## Design

### Agent file

```yaml
planning:                 # optional; omitted = no planning call
  enabled: true           # default false
  prompt: |               # optional; "" = harness.DefaultPlanPrompt
    Before you start, write a short plan ...
  max_tokens: 1024        # optional; 0 = harness.DefaultPlanMaxTokens (1024); 0..1000000
```

```go
// Planning configures the optional planning call (task Q.4). Off by default.
type Planning struct {
	Enabled   bool   `yaml:"enabled"`
	Prompt    string `yaml:"prompt"`     // "" = the harness default
	MaxTokens int    `yaml:"max_tokens"` // 0 = the harness default
}
```

Add `Planning Planning \`yaml:"planning"\`` to `Agent` after `Context`. No
defaults in `agentconfig` (the harness owns them). `validatePlanning`, called
from `Validate` after `validateContext`: if `MaxTokens < 0 || MaxTokens > 1000000`
→ `p.add("planning.max_tokens", "must be between 0 and 1000000, got %d", v)`.
Unknown keys under `planning:` are already rejected by the strict decoder.

`harnessConfig` adds:
`Plan: harness.PlanConfig{Enabled: a.Planning.Enabled, Prompt: a.Planning.Prompt, MaxTokens: a.Planning.MaxTokens},`
`adlc run`, `adlc resume` and `adlc eval` all use `harnessConfig`, so nothing else changes in `internal/cli`.

### Harness API (`internal/harness/harness.go`)

```go
// PlanCallID is the call id of the planning call; its step is 0 (task Q.4).
const PlanCallID = "plan"

// DefaultPlanMaxTokens is PlanConfig.MaxTokens when it is zero.
const DefaultPlanMaxTokens = 1024

// DefaultPlanPrompt is PlanConfig.Prompt when it is empty.
const DefaultPlanPrompt = "Before you start, write a short plan for this task: a numbered list of at most 7 steps, one line each, naming the files or tools you expect to use. Do not start the work yet and do not call any tools; reply with the plan only."

// PlanFollowUp is the user message Transcript puts after the plan, so that a
// request never ends with the assistant's own message.
const PlanFollowUp = "Now carry out the plan above. Change it if you learn something that makes it wrong."

// PlanConfig controls the planning call before step 1 (task Q.4). The zero
// value means no planning call.
type PlanConfig struct {
	Enabled   bool
	Prompt    string // sent after the task in the plan request; "" = DefaultPlanPrompt
	MaxTokens int    // output limit of the plan call; 0 = DefaultPlanMaxTokens
}
```

`Config` gets `Plan PlanConfig // task Q.4` after `Context`. `applyDefaults`:
empty `Plan.Prompt` → `DefaultPlanPrompt`; `Plan.MaxTokens == 0` →
`DefaultPlanMaxTokens` (always, also when disabled). `validate`: `Plan.MaxTokens < 0`
→ `errors.New("harness: config: plan max tokens must be >= 0")`.

### The plan request (`planRequest`)

```go
&model.Request{
	Model: l.cfg.Model, System: l.cfg.System, MaxTokens: l.cfg.Plan.MaxTokens, Effort: l.cfg.Effort,
	Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{
		model.TextPart(l.r.state.Task), model.TextPart(l.cfg.Plan.Prompt),
	}}},
}
```

`Tools` stays nil. The task comes from `l.r.state.Task` (set by `run_started`), so resume works.

### `runPlan` (`plan.go`), numbered

`func (l *loop) runPlan() error` — sets `l.final` when the run stops. (Enum names below omit the
`adlcv1.StopReason_` / `adlcv1.FinishReason_` prefix.)

1. If `l.ctx.Err() != nil`: `return l.finish(STOP_REASON_CANCELLED, "", "")`.
2. If `e := beforeModelCall(l.cfg.Budget, l.usage()); e != nil`: `return l.finishBudget(e)`.
3. `req, err := l.compact(l.planRequest())`; if `err != nil || l.final != nil` return `err`.
4. `resp, err := l.callModel(req, PlanCallID, 0)`; if `err != nil || resp == nil` return `err` (retries, failures and cancellation work exactly as for a step).
5. `l.recordResponse(PlanCallID, resp, nil)` — `nil` calls: tool calls in a plan response are dropped (the request offered no tools). Return its error if any.
6. If `resp.Finish == FINISH_REASON_REFUSAL`: `return l.finish(STOP_REASON_ERROR, "", "model refused")`.
7. Otherwise return nil. `MAX_TOKENS` is not an error here: the partial plan is kept. No checkpoint is written after the plan.

`loop.run`: after `l.loadTools()`, add
`if l.cfg.Plan.Enabled { if err := l.runPlan(); err != nil || l.final != nil { return l.final, err } }`
then `return l.steps(1)` as before.

`func hasPlan(events []*adlcv1.Event) bool` — true if any event is a
`model_call_completed` with `CallId == PlanCallID`.

### Steps are not counted for the plan

`recorder.modelCalls` is what `usage().Steps` (max_steps) and Resume's
`next_step` use. Add `func countsAsStep(ev *adlcv1.Event) bool` (true for a
`model_call_completed` whose `CallId != PlanCallID`) and use it in both
`newRecorderFrom` and `add` instead of `GetModelCallCompleted() != nil`.
Tokens and cost need no change: they come from `RunState.Usage`, which already
adds the plan event's `cost`.

### Transcript

In the `*adlcv1.Event_ModelCallCompleted` case, after `flush()`:

1. If `CallId != PlanCallID`: append `assistantMessage(...)` as today.
2. If `CallId == PlanCallID` and `Text != ""`: append
   `{RoleAssistant, [TextPart(text)]}` then `{RoleUser, [TextPart(PlanFollowUp)]}`.
   Provider blobs of the plan are not replayed.
3. If `CallId == PlanCallID` and `Text == ""`: append nothing.

A run with a plan gives step 1 the messages `[user task, assistant plan, user PlanFollowUp]`;
later steps append to that, so the prefix property of task 1.6.2 still holds.

### Resume

1. `lastModelCompleted` skips events whose `model_call_completed.CallId == PlanCallID`
   (`continue`). So a run that died right after its plan is case C/D ("no
   response yet") and continues with step 1, instead of finishing with the plan as its result.
2. In `(*loop).resume`, after `closeInFlight` succeeds and before `return l.steps(nextStep)`:
   `if l.cfg.Plan.Enabled && l.r.modelCalls == 0 && !hasPlan(l.r.events) { if err := l.runPlan(); err != nil || l.final != nil { return l.final, err } }`.
   So a run that died during the plan call makes the plan call again (same call id `plan`;
   the trace builder already marks the first span "superseded").
3. `nextStep` stays `modelCalls + 1`; with step 2 above it ignores the plan.

### `interfaces.md` text to add

§6, in the `Config` block after `Context`: `Plan PlanConfig // task Q.4: planning call before step 1; zero = off`
and after the block: the `PlanCallID`, `DefaultPlanMaxTokens`, `DefaultPlanPrompt`, `PlanFollowUp`
constants and the `PlanConfig` type exactly as above.
§6.1, before `checkpoint`: `When Config.Plan.Enabled, before step 1: model_call_started {call_id: "plan", step: 0}
… model_call_completed (no tool calls, no checkpoint). It counts towards tokens and cost, not max_steps.
Transcript turns it into an assistant message followed by the user message PlanFollowUp.`
§7: `Task Q.4 adds the optional planning: section (enabled, prompt, max_tokens 0..1000000); harnessConfig maps it to harness.Config.Plan.`

## Steps

1. Branch from the latest `main` (AGENTS.md).
2. Add the constants, `PlanConfig`, `Config.Plan`, defaults and validation to `harness.go`.
3. Write `TestTranscriptPlanMessages` and `TestTranscriptEmptyPlanAddsNothing`; see them fail; change `transcript.go`; pass.
4. Change `recorder.go` (`countsAsStep`).
5. Write `plan.go`; change `loop.run`.
6. Write `plan_test.go` (harness tests below); pass.
7. Change `resume.go`; write the three resume tests; pass.
8. Agent config: `Planning`, `validatePlanning`, tests, `full.yaml` block (`enabled: false`, with comments like the other sections).
9. `harnessConfig` and `wiring_plan_test.go`.
10. `interfaces.md`.
11. Break each new piece on purpose once (for example count the plan in `countsAsStep`), see a test fail, restore.
12. `make fmt && make ci`.

## Tests

All harness tests use `model.NewFake` via `runHarness`; `cfg := baseConfig(); cfg.Plan = PlanConfig{Enabled: true}` unless said otherwise. `plan := textTurn("1. read a.go")`. "Event N" means the event with seq N (`r.events[N-1]`).

| Test | Asserts |
|------|---------|
| `TestPlanDisabledChangesNothing` | `baseConfig()` (Plan zero), turns `toolTurn(readCall("t1","a.go")), textTurn("ok")`: kinds exactly `run_started, model_call_started, model_call_completed, tool_call_requested, policy_decided, tool_call_completed, checkpoint, model_call_started, model_call_completed, run_completed`; 2 requests; request 0 has 1 message and 1 tool; no event has call id `plan` |
| `TestPlanEnabledRecordsPlanCall` | turns `plan, toolTurn(readCall("t1","a.go")), textTurn("ok")`: kinds `run_started, model_call_started, model_call_completed, model_call_started, model_call_completed, tool_call_requested, policy_decided, tool_call_completed, checkpoint, model_call_started, model_call_completed, run_completed`; event 2 has call id `plan`, step 0, attempt 1; event 3 has call id `plan`, text `1. read a.go`, no tool calls; event 4 has call id `m1`, step 1; checkpoint step 1; `state.Step == 2`; stop COMPLETED result `ok` |
| `TestPlanRequest` | table: `{}` → request 0 has `Tools` empty, `MaxTokens 1024`, `System == cfg.System`, 1 message with parts `[task, DefaultPlanPrompt]`; `{Prompt: "plan please", MaxTokens: 50}` → parts `[task, "plan please"]`, `MaxTokens 50` |
| `TestPlanInEveryLaterRequest` | turns `plan`, two `toolTurns`, `textTurn("ok")`: every request i ≥ 1 has `Messages[1]` = assistant text `1. read a.go`, `Messages[2]` = user text `PlanFollowUp`, and 1 tool; `checkPrefix` holds for every pair (i-1, i) with i ≥ 2 |
| `TestPlanNotCountedAsStep` | `Budget{MaxSteps: 2}`, turns `plan` + `toolTurns(5, Usage{})`: `countModelCalls == 3`; `wantBudgetStop(budgetSteps, "max_steps 2 reached")` |
| `TestPlanCountsTowardTokens` | `Budget{MaxTokens: 100}`, plan usage `{InputTokens: 40, OutputTokens: 20}`, turns `toolTurns(5, Usage{InputTokens: 40, OutputTokens: 20})`: `countModelCalls == 2` (plan + m1); `wantBudgetStop(budgetTokens, "max_tokens 100 reached (used 120)")` |
| `TestPlanCountsTowardCost` | `Price{InputPerMTok: 6_000_000}`, `Budget{MaxUsdMicros: 10}`, plan usage `{InputTokens: 1}`, `toolTurns(5, Usage{InputTokens: 1})`: `countModelCalls == 2`; `wantBudgetStop(budgetCost, "max_usd_micros 10 reached (used 12)")`; plan event cost `usd_micros 6` |
| `TestPlanToolCallsDropped` | plan turn `toolTurn(readCall("t1","a"))`, then `textTurn("ok")`: no `tool_call_requested` in the log; plan `model_call_completed` has no tool calls; request 1 has exactly 1 message (empty plan text adds nothing) |
| `TestPlanRefusalStopsRun` | plan turn finish `FINISH_REASON_REFUSAL`: kinds `run_started, model_call_started, model_call_completed, run_completed`; `wantStop(ERROR, "model refused")`; 1 request |
| `TestPlanRetried` | `deps.Sleep = (&sleepRecorder{}).sleep`, turns `rateLimited(), plan, textTurn("ok")`: a `model_call_failed` with call id `plan`; second `model_call_started` is `plan`, step 0, attempt 2; stop COMPLETED |
| `TestTranscriptPlanMessages` (transcript_test) | events `evStarted()`, `evModel({CallId: "plan", Text: "P"})`, `evModel({CallId: "m1", Text: "done"})` → `[user "task", assistant "P", user PlanFollowUp, assistant "done"]` |
| `TestTranscriptEmptyPlanAddsNothing` | plan with `Text: ""` → `[user "task", assistant "done"]` |
| `TestResumeAfterPlanContinuesAtStep1` | plan enabled; `crashRun(t, 3, cfg, …, plan, textTurn("ok"))` (log ends after plan completed); `resumeOK(…, textTurn("ok"))`: tail kinds `run_resumed, model_call_started, model_call_completed, run_completed`; `run_resumed.next_step == 1`; tail model call id `m1`; resumed request 0 has `Messages[1]` text `1. read a.go`; result `ok` |
| `TestResumeDuringPlanRepeatsPlan` | `crashRun(t, 2, …)` (only plan started); resume with `plan, textTurn("ok")`: tail kinds `run_resumed, model_call_started, model_call_completed, model_call_started, model_call_completed, run_completed`; first tail call id `plan` step 0, second `m1`; `next_step == 1` |
| `TestResumeAfterStep1DoesNotReplan` | turns `plan, twoReads()...`; crash after 5 events (inside step 1); resume with `twoReads()[1:]...`: no tail event has call id `plan`; stop COMPLETED |
| `TestPlanningConfig` (agentconfig) | no section → `Planning{}`; `planning: {enabled: true, prompt: "p", max_tokens: 10}` loads as given; `max_tokens: -1` → error contains `planning.max_tokens: must be between 0 and 1000000, got -1`; `planning: {enabld: true}` → contains `unknown field "enabld"` |
| `TestHarnessConfigPassesPlanning` (cli) | agent with `planning: {enabled: true, max_tokens: 10}` → `cfg.Plan == harness.PlanConfig{Enabled: true, MaxTokens: 10}` |

Every existing test must pass unchanged: that is the "disabled changes nothing" golden.

## Verify

```bash
go test ./internal/harness/ -run 'Plan|Transcript|Resume' -v
go test ./internal/agentconfig/ ./internal/cli/ -run 'Planning' -v
make ci
```

Expected: every test `PASS`; `make ci` ends with `0 issues.` and no stale `gen/`.

## Out of scope

- A different model for the plan call (task Q.5).
- Re-planning during the run, plan critique, plan checklists.
- `events.proto` changes (the plan uses existing events).
- A planning section in the files under `agents/` (an experiment adds it later).

## Pitfalls

- `resume.go` already has `planRecovery` / `recoveryPlan` (crash recovery). They are unrelated; do not reuse or rename them.
- Do not count the plan with `RunState.ModelCalls` or `RunState.Step` for max_steps; only `recorder.modelCalls` decides, and it must skip `PlanCallID` in both `add` and `newRecorderFrom`.
- A request must never end with an assistant message (some providers treat it as prefill): that is why `PlanFollowUp` exists. Do not skip it.
- Never put the plan, the step number or the planning prompt into `Request.System`.
- Keep `Request.Tools` nil for the plan call, not `l.specs`.
- Use `continue` inside the `for` loop of `lastModelCompleted`, not `break`.
- The fake script for a planning run needs one extra turn at the start (the plan).

## Done checklist

- [ ] `planning:` section parsed, validated, documented in `full.yaml`, mapped by `harnessConfig`
- [ ] Plan call recorded as `plan` / step 0, no tools, no checkpoint
- [ ] Plan in every later request, followed by `PlanFollowUp`
- [ ] Counts towards tokens and cost, not `max_steps`
- [ ] Resume: continues after a plan, repeats an unfinished plan, never plans twice
- [ ] Disabled: all existing tests unchanged and passing
- [ ] `interfaces.md` §6, §6.1, §7 updated
- [ ] `make ci` passes
