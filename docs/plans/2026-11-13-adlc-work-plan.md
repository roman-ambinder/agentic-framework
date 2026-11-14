# ADLC — Work Plan

Supersedes the sequencing (not the task content) of `2026-11-13-adlc-m0-m1-plan.md`.
Implementation briefs, task index and shared interfaces: `docs/impl/` (start with `AGENTS.md` at the repo root).
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
| 0.0 | Move to Go 1.26, pin all tools | ✅ merged (PR #5) |
| 0.4, 1.4.1, E.1 | Wave 1: model package, Docker sandbox, ledger fixture | ✅ merged (PR #6) |
| 0.5.1–0.5.4, 0.5b, 0.6, 0.7.1, 0.7.2, 0.8, 0.9.1, 0.9.2, 0.10, 0.11.1, 0.11.2 | Rest of M0 | ✅ on `integration` |
| E.2, E.3 | Seeded bugs, suite loader and self-check | ✅ on `integration` |
| 1.1–1.3, 1.4.2, 1.5, 1.6.1, 1.6.2, 1.7, 1.8, 1.9, 1.11 | M1 code tasks | ✅ on `integration` |
| G.1–G.4 | Gaps found in M0/M1: sandbox for `adlc run`, hooks in the agent file, CLI consistency, context keys | ✅ merged |
| C.1–C.4 | M2-C control plane: approvals, policy v2, kill switch, agent registry and promotion | ✅ merged (PR #11) |
| Q.1, Q.2 | M2-Q: pass@k and `adlc eval-compare`, failure analysis | ✅ merged (PR #11) |
| Q.3–Q.5 | M2-Q: fork a run, plan step, model per step role | ✅ on `integration` |
| W.1–W.6 | M3: workflow file, events, engine, review loop, gates and CLI, SDLC pipeline (`docs/impl/m3.md`) | 📝 briefed; build after G1 |
| P.1–P.3, P.5a–P.5b | Sandbox pause, files path: a run parked for approval frees its container and restores `/workspace` on approve | ✅ done (P.4, process checkpoints, open) |
| E.4 | Manual baseline (a person runs it; sets gate G1's numbers) | ⏳ open |
| 1.10 | Coder agent iteration (needs model API keys and a Docker host) | ⏳ open |

M0 demo works: `adlc run examples/hello.yaml --task "What does greeting.txt say?" --fake-script examples/hello.fake.json`, then `adlc trace <run>` and `adlc cost`. Gate G0 (a real model through the loop) needs an API key and is not recorded yet.

## 3. M0 — Walking skeleton

**Goal:** an agent defined in YAML runs end to end through the real loop,
first on a scripted fake model and then on one real model, with every step in
the event log and a readable trace.

**Demo:** `adlc run examples/hello.yaml --task "What does greeting.txt say?" --fake-script examples/hello.fake.json`,
then the same task with `examples/hello-anthropic.yaml` and `ADLC_ANTHROPIC_MODEL` set; both print a run
summary with cost, and `adlc trace <run>` prints the span tree from the log.

Tasks (sizes, dependencies and a brief for each: `docs/impl/README.md`):

- **0.0** upgrade to Go 1.26 — needed first: no release of the Anthropic Go SDK v2 supports Go 1.24
- **0.4** model types, `Adapter` interface, scripted fake model
- **0.6** policy client with static rules · **0.7.1** tool router core · **0.7.2** MCP tool router · **0.8** fs MCP server
- **0.5.1–0.5.4** harness: core loop, budgets, retries, hooks and checkpoints
- **0.5b** minimal Anthropic adapter (moved forward from M1)
- **0.9.1** `agent.yaml` loader · **0.9.2** `adlc run` · **0.10** trace export and `adlc trace`
- **0.11.1–0.11.2** plugin contract, Go launcher, Python hook plugin

**Critical path:** 0.0 → 0.4 → 0.6 / 0.7.1 → 0.5.1 → 0.5.4 → 0.9.2 → 0.10.

**Gate G0 (after M0):** does a real model complete a simple file task through
the loop without harness bugs? Record: steps used, tokens, cost, any stop reason
other than `completed`. If the loop needs rework, fix it before M1.

## 4. Eval track — starts now, runs alongside M0

Fixtures don't depend on the framework, so build them early: when the coder
agent first runs, there's a score waiting for it.

- **E.1** fixture repository (small stdlib-only Go project, its own module)
- **E.2** 10 seeded bugs (3 easy, 4 medium, 3 hard), each with issue text, bug patch, reference fix and hidden tests
- **E.3** fixture format, loader, and a check that every bug's hidden tests fail with the bug and pass with the fix
- **E.4** manual baseline, which sets the numbers for gate G1

## 5. M1 — One agent fixes one issue

**Goal:** a coder agent in a Docker sandbox fixes seeded bugs, scored by the
eval suite, with both providers compared.

**Demo:** `adlc eval evals/suites/go-bugs --agents agents/sdlc/coder.anthropic.yaml,agents/sdlc/coder.openai.yaml` prints pass@1,
cost and steps per fixture and provider.

- **1.1** Anthropic adapter: streaming, prompt caching, token counting · **1.2** OpenAI adapter · **1.3** model router and fallback
- **1.4.1** sandbox interface and Docker backend · **1.4.2** isolation (network off, limits, copied workspace) · **1.5** shell/git/search tools in the sandbox
- **1.6.1** context manager budgets and compaction · **1.6.2** stable prefix for caching
- **1.7** resume a crashed run · **1.8** eval runner · **1.9** graders and report · **1.10** coder agent · **1.11** `adlc cost`

**Critical path:** 1.4.1 → 1.4.2 → 1.5 → 1.8 → 1.9 → 1.10. Adapters (1.1–1.3) and context (1.6.x) run alongside.

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
- **Briefs:** `docs/impl/m3.md` (shared decisions and API) and tasks W.1–W.6. The demo ends with a local branch `adlc/<workflow-id>`; pushing it and opening the PR stay with the person (design §3.6).

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
- **Go version:** the repo targets Go 1.26 (toolchain go1.26.11, task 0.0). Tools are pinned in `tooling/go.mod`.

## 8. Decision log

| Date | Gate / decision | Result |
|------|-----------------|--------|
| 2026-11-13 | Pull one real adapter (0.5b) into M0; start the eval track alongside M0 | Adopted in this plan |
| 2026-11-13 | Move to Go 1.26 before further M0 work (task 0.0) | Every Anthropic Go SDK v2 release needs Go ≥ 1.25; latest OpenAI Go SDK v3 needs Go 1.26 |
| 2026-11-13 | Split large tasks (0.5, 0.7, 0.9, 0.11, 1.4, 1.6) and write an implementation brief per task (`docs/impl/`) | So less capable models can implement tasks without making design decisions |

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
