# ADLC — Work Plan

Supersedes the sequencing (not the task content) of `2026-11-13-adlc-m0-m1-plan.md`.
Design: `docs/design/2026-11-13-adlc-framework-design.md`.

## 1. How this plan works

**No deadline, so the plan is ordered by learning.** The biggest unknown is
not whether the plumbing can be built, but whether an agent built on it fixes
real bugs at an acceptable cost. Work that answers that sooner comes first.

**Three levels of detail, on purpose.**
- M0–M1: task level, with sizes, dependencies and acceptance criteria.
- M2–M3: epic level, with a goal, a demo and the questions M1 must answer first.
- M4 and later: options plus the trigger that would make each one worth doing.

Later milestones are re-planned at each gate, using what was learned.

**Every milestone ends with a demo and a gate.** A demo is a command you can
run that shows the milestone working. A gate is a decision based on measured
results, written down in §8 before work moves on.

**Sizes are relative effort, not dates.**

| Size | Meaning |
|------|---------|
| S | Small, well understood; one focused session |
| M | A few sessions; one new idea or library |
| L | Several sessions; design choices inside it. Split if it grows |

**Working rules**
- One task in progress at a time; one PR per task (as for 0.1–0.3).
- A task is done when `make ci` passes and its acceptance criterion is shown by a test or a demo.
- Live model calls only behind `-tags live`, with a spend cap set per run (§7).

## 2. Status

| Task | | State |
|------|---|-------|
| 0.1 | Repo bootstrap, CLI, CI | ✅ merged (PR #1) |
| 0.2 | Event schema (protobuf), buf lint/breaking | ✅ merged (PR #1) |
| 0.3 | Append-only SQLite event log + fold | ✅ merged (PR #2) |

## 3. M0 — Walking skeleton

**Goal:** an agent defined in YAML runs end to end through the real loop,
first on a scripted fake model and then on one real model, with every step in
the event log and a readable trace.

**Demo:** `adlc run examples/hello.yaml --model fake` and then `--model anthropic`;
both print a span tree with cost, and `adlc trace <run>` replays it from the log.

| # | Task | Size | Depends on | Done when |
|---|------|------|-----------|-----------|
| 0.4 | `ModelAdapter` interface, normalized types (`Message`, `ToolCall`, `ToolResult`, `ModelEvent` with opaque provider blobs), scripted fake adapter | M | 0.2 | Fake replays a scripted conversation; adapter contract test suite exists (runs against the fake) |
| 0.5 | Harness loop: budgets (steps, tokens, wall-clock, $, tool calls), retry with backoff, stop reasons, `pre_tool`/`post_tool` hooks, writes every step to the event log, checkpoints | L | 0.3, 0.4 | Table test per stop reason on the fake; a run's log folds to the expected `RunState` |
| **0.5b** | **Moved forward:** Anthropic adapter, minimal (non-streaming first, tool calls, refusal handling, usage → `Cost`) | M | 0.4 | Passes the adapter contract suite with recorded responses; one live smoke test under `-tags live` |
| 0.6 | `PolicyClient` interface + local static rules on risk classes | S | 0.5 | A denied call ends the run with `denied`; decision recorded as `policy_decided` |
| 0.7 | Tool router: MCP client (stdio), namespacing, risk class from config, output cap | L | 0.5 | Talks to a test MCP server; oversized output capped for the model, full output in the log |
| 0.8 | `tools/fs` MCP server (read/list/write under a root) | M | 0.7 | Path escapes (`../`, symlinks) rejected in tests |
| 0.9 | `agent.yaml` schema, loader, validation; `adlc run` command | M | 0.5, 0.7 | Bad configs fail with clear messages; `adlc run` works on the fake |
| 0.10 | Observability v0: event log → OpenTelemetry spans (stdout exporter), `adlc trace` | M | 0.3, 0.9 | Demo above prints a span tree with costs |
| 0.11 | Cross-language check: `plugin.proto` `Hook` service, plugin launcher, one Python hook plugin | M | 0.5 | Integration test: the Python hook blocks a tool call |

**Critical path:** 0.4 → 0.5 → 0.7 → 0.9 → 0.10. 0.5b, 0.6, 0.8 and 0.11 can be done in any order once their dependency is merged.

**Gate G0 (after M0):** does a real model complete a simple file task through
the loop without harness bugs? Record: steps used, tokens, cost, any stop reason
other than `completed`. If the loop needs rework, fix it before M1.

## 4. Eval track — starts now, runs alongside M0

Fixtures don't depend on the framework, so build them early: when the coder
agent first runs, there's a score waiting for it.

| # | Task | Size | Depends on | Done when |
|---|------|------|-----------|-----------|
| E.1 | Fixture repo: a small Go project (a few packages, a real test suite, about 1–2k lines) | M | — | `go test ./...` passes on the clean repo |
| E.2 | 10 seeded bugs, graded easy / medium / hard: each is a patch that breaks the repo, an issue text written as a user would, and hidden tests that fail on the bug and pass on the fix | L | E.1 | For each bug: hidden tests fail with the bug applied, pass on the reference fix |
| E.3 | Fixture format + loader (`suites/go-bugs/<id>/{issue.md, bug.patch, hidden_test.go, meta.yaml}`) | S | E.2 | Loader validates every fixture in CI |
| E.4 | Baseline: run each fixture by hand with a general-purpose coding agent, note what it takes | S | E.2 | Short notes per fixture; tells you whether the suite is too easy or too hard |

## 5. M1 — One agent fixes one issue

**Goal:** a coder agent in a Docker sandbox fixes seeded bugs, scored by the
eval suite, with both providers compared.

**Demo:** `adlc eval suites/go-bugs --providers anthropic,openai` prints pass@1,
cost and steps per fixture and provider.

| # | Task | Size | Depends on | Done when |
|---|------|------|-----------|-----------|
| 1.1 | Anthropic adapter, full: streaming, prompt caching, `CountTokens`, provider blobs replayed unchanged | M | 0.5b | Contract suite passes; cache-read tokens show up in cost events across steps |
| 1.2 | OpenAI adapter (configurable base URL) | M | 0.4 | Same contract suite passes |
| 1.3 | Model router: per-role / per-step model choice, fallback to the other provider | S | 1.1, 1.2 | Fallback test with a failing fake |
| 1.4 | Docker sandbox backend: create/exec/destroy, CPU/memory limits, network off, repo mounted as a working copy | L | 0.5 | `go test` runs in the container; outbound network fails |
| 1.5 | `tools/shell`, `tools/git`, `tools/search` running inside the sandbox | M | 0.7, 1.4 | Agent can only run commands inside the container |
| 1.6 | Context manager v1: per-section token budgets, compaction of old tool output, stable prefix for caching | L | 0.5, 1.1 | A long run stays under the context limit; cache hit rate visible in `adlc cost` |
| 1.7 | Resume a crashed run from its last checkpoint (uses `RunState.Digest`) | M | 0.5, 1.4 | Kill mid-run, `adlc resume <run>` completes it |
| 1.8 | Eval runner: apply bug, run agent, apply hidden tests, record results as events | M | E.3, 1.5 | `adlc eval` runs one fixture end to end |
| 1.9 | Graders: outcome (hidden tests, `go vet`, diff applies) + trajectory (steps, wasted calls, policy violations); JSON report | M | 1.8 | Report shows both; JSON usable in CI |
| 1.10 | `agents/sdlc/coder.yaml` + prompt; iterate against the suite | L | 1.8 | Meets the threshold set at G1 |
| 1.11 | `adlc cost`: per run / model / fixture from cost events | S | 0.3 | Totals match the sum of events |

**Critical path:** 1.4 → 1.5 → 1.8 → 1.10. Adapters (1.1–1.3) and context (1.6) run alongside.

**Gate G1 (after M1) — decides what M2 is.** Measure on the full suite:
pass@1 per difficulty, median cost and steps per fixture, and the top 3 causes of failure.

| If… | Then M2 is… |
|-----|-------------|
| pass@1 is low and failures are mostly agent/context quality | **M2-Q: agent quality** — context strategy, tool design, prompts, planning step |
| pass@1 is acceptable but cost or steps are high | **M2-Q** with the focus on cost: caching, model routing, compaction |
| pass@1 and cost are acceptable | **M2-C: control plane**, as planned below |

Set the numeric thresholds after E.4, once the suite's difficulty is known.

## 6. Later milestones (epic level)

### M2-C — Control plane v1
- **Goal:** central policies, approvals and kill switch, so runs can safely do write-external actions.
- **Epics:** policy engine on risk classes (+ rule tests); approval queue with park/resume (`adlc approve`); kill switch per run / agent / global; agent registry with versions, promotion gated on eval results.
- **Demo:** an agent that opens a PR parks for approval, resumes after `adlc approve`, and a kill switch stops a running agent within one step.
- **Needs from M1:** which tool calls actually needed a human, and how often.

### M2-Q — Agent quality (if G1 says so)
- **Goal:** raise pass@1 and/or lower cost per fixture, measured on the suite.
- **Epics:** context strategy experiments; tool redesign from trajectory data; plan-then-act step; model routing by step type.
- **Demo:** before/after report on the same suite.
- **Rule:** every change is an experiment with a recorded before/after.

### M3 — Orchestrator and the SDLC pipeline
- **Goal:** spec → code → test → review agents coordinated by a task graph, with a human gate before release.
- **Epics:** task graph (DAG) with nested budgets; review loop (producer ↔ critic); SDLC agent roles from design §4; pipeline-level evals.
- **Demo:** an issue goes through spec, code, review and an approval gate, and ends as a PR.
- **Needs first:** evidence from M1/M2 that a single agent is reliable enough to be worth chaining; a multi-agent pipeline multiplies single-agent failure rates.

### M4 and later — options with triggers

| Option | Do it when… |
|--------|-------------|
| Generated Python / TypeScript SDKs | Someone wants to drive agents from another language |
| More non-Go plugins (adapter, grader) | A needed provider or grader library only exists outside Go |
| Postgres backend | More than one machine needs the same event log |
| Kubernetes sandbox backend | Runs need more parallelism than one machine gives |
| Web UI for approvals and traces | CLI approvals become a bottleneck |
| Cross-run memory | Trajectory data shows agents repeating the same discoveries |
| Multi-user control plane | A second person uses it |

## 7. Cross-cutting work

- **Live-test spend:** every live run has a `$` budget in its agent config; `make eval-live` refuses to run without one. Track total spend in `adlc cost`.
- **Security of the sandbox:** network off by default, no host credentials mounted, changes leave only as a diff. Review this at the end of M1 before any write-external tool exists.
- **Releases:** tag `v0.1.0` after M0, `v0.2.0` after M1; changelog from PR titles.
- **Docs:** each milestone updates the design doc where reality differs from it.
- **Go version:** the repo targets Go 1.24, which has held back `buf`, `modernc.org/sqlite` and other tools. Decide before M1 whether to move to a newer Go.

## 8. Decision log

| Date | Gate / decision | Result |
|------|-----------------|--------|
| 2026-11-13 | Pull one real adapter (0.5b) into M0; start the eval track alongside M0 | Adopted in this plan |

## 9. Risks

| Risk | Signal | Mitigation |
|------|--------|-----------|
| Building platform without learning whether agents work | M0 drags on with no real-model runs | 0.5b in M0; gate G0 |
| Fixture suite too easy or too hard to tell anything | E.4 baseline: everything passes or nothing does | Grade fixtures by difficulty; adjust before G1 |
| Provider-neutral contract hides features that matter (caching, thinking) | Adapter needs workarounds | Capability flags + opaque blobs (already in the schema) |
| Live eval cost grows unnoticed | Spend per suite run trending up | Per-run `$` budget, `adlc cost` |
| Multi-agent pipeline (M3) before single-agent reliability | Starting M3 with low G1 numbers | M3 needs a G1/M2 result first |

## 10. Parked ideas
- Replaying recorded runs against a new model or prompt (design §3.8) — becomes valuable at M2-Q.
- Reviewer agent as a grader for qualitative output — after M3 has a reviewer role.
- Running the SDLC agents on this repository itself — a good real-world eval once M3 exists.
