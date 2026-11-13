# ADLC Framework — Design Draft (brainstorm v0.1)

Status: draft for discussion. Nothing here is built yet.

## 0. Decisions so far

| # | Decision | Source |
|---|----------|--------|
| D1 | Two layers: a general **agent platform**, with an **SDLC agent team** as the first reference workload | User |
| D2 | **Own the core, adopt standards** (MCP for tools, OpenTelemetry for telemetry, OCI containers for sandboxes) | User |
| D3 | **Provider-agnostic** model layer | User |
| D4 | **Core written in Go**; every extension point stays open to other languages | User |
| D5 | **Single user (the author)** at first; no multi-tenancy | User |
| D6 | **Runs locally** at first | User |

### Defaults I picked (override any of them)

| # | Default | Why | Alternative |
|---|---------|-----|-------------|
| A1 | Contracts in **Protobuf (gRPC)** + **JSON Schema** for tool/agent definitions | Language-neutral; the core and every plugin share one source of truth | OpenAPI only |
| A2 | **One `adlc` binary** with the control plane embedded; Docker sandbox; SQLite | Easiest local setup; Postgres/Kubernetes later behind the same interfaces | Separate services from day one |
| A3 | Milestone 1 = **one agent fixes one issue** end-to-end, fully traced and within budget | Exercises all 8 layers with the least orchestration | Multi-agent pipeline first |
| A4 | Identity/policy interfaces kept even though there is one user | Adding users later doesn't need a rewrite; the control plane stays a mode of the binary, not a separate system | Drop identity entirely for V1 |

### What "open to other languages" means concretely

Go owns the loop, the orchestrator, the event log and the control plane. Anything a user might want to write in another language goes through one of these extension points:

| Extension point | Protocol | Example in another language |
|-----------------|----------|-----------------------------|
| **Tools** | MCP (stdio or HTTP) | A Python MCP server that queries a data warehouse |
| **Model adapters** | gRPC plugin process (`ModelAdapter` service) | A TS adapter for a provider with no Go SDK |
| **Hooks / guards** | gRPC plugin (`Hook` service) | A Python PII-redaction hook |
| **Eval graders** | gRPC plugin (`Grader` service) or a CLI that reads JSON | A Python grader using an ML library |
| **Agents as code** | Client SDK against the harness API (start run, stream events, send approvals) | A TS app that drives agents |
| **Agents as config** | YAML/JSON agent + workflow definitions | No code needed in any language |
| **Event consumers** | OpenTelemetry export + JSONL event stream | Any analytics stack |

Plugins run as **separate processes** that the core launches and talks to over gRPC on a local socket (the same pattern Terraform uses for its providers). A crash in a plugin cannot take down the core, and plugins written in Go are just in-process implementations of the same interfaces.

## 1. Core idea: the event log is the source of truth

Every meaningful thing appends an immutable event to a **run log**: model call, tool call, tool result, policy decision, approval, checkpoint, error.

- **State** = a fold over the events, so a run can be resumed after a crash.
- **Observability** = the same events exported as OpenTelemetry spans.
- **Evaluation** = graders read the log and the final artifacts.
- **Audit** = the log, plus who or what approved each action.

This one decision connects layers 4, 6, 7 and 8 so they don't each need their own record-keeping.

```
Event { run_id, seq, time, agent_id, parent_seq, cost{input/output/cache tokens, usd_micros}, trace, oneof payload }
```
Defined in `contracts/proto/adlc/v1/events.proto`. The payload is a typed `oneof` (one message per event kind) rather than a type string plus untyped payload, so every language gets typed access. Money is stored as integer micro-dollars, never floating point.

## 2. Layering and dependency direction

```
            ┌──────────────────── Control plane ────────────────────┐
            │ identities · policies · budgets · approvals · registry │
            └───────▲───────────────────────▲───────────────────────┘
                    │ authorize / charge     │ deploy / configure
┌──────────── Orchestrator (task graph, delegation) ────────────┐
│   spawns ▼                                                     │
│  ┌────────────── Harness / agent loop ──────────────┐          │
│  │ Model adapter ◄─► loop ◄─► Tool router (MCP)      │          │
│  │            Context manager / memory / state       │          │
│  └──────────────────────┬───────────────────────────┘          │
└─────────────────────────┼──────────────────────────────────────┘
                          ▼ executes in
                 Runtime / sandbox (container per run)
   Everything ──► Event log ──► Observability (OTel) + Evaluation
```

Rule: lower layers never import higher ones. The harness asks the control plane for decisions through one narrow interface (`PolicyClient`) and never knows how those decisions are made.

## 3. Components

### 3.1 Model layer
**Job:** interpret the task, pick actions, produce answers.
```go
type ModelAdapter interface {
    Generate(ctx context.Context, req *ModelRequest) (<-chan ModelEvent, error) // streaming
    Capabilities() ModelCaps // tool calling, max context, caching, thinking, vision
    CountTokens(ctx context.Context, req *ModelRequest) (int, error)
}
```
The same interface is also defined as a gRPC service in `contracts/`, so adapters can be written in other languages (see §0).
- A normalized message and tool-call schema inside the framework; each adapter translates to and from its provider.
- `ModelCaps` lets the harness degrade gracefully (e.g. no native tool calling → structured-output fallback).
- A **model router** chooses by policy: cheap model for routine steps, strong model for planning and review, fallback on outage.
- V1: two adapters (one hosted provider + one OpenAI-compatible endpoint, which covers many local servers).

### 3.2 Harness / agent loop
**Job:** `call model → run tool → read result` until done.
```
loop:
  check budgets (steps, tokens, wall-clock, $)  → stop with reason if exceeded
  ctx  = context_manager.build(state)
  out  = model.generate(ctx)                     (retry with backoff on transient errors)
  if out.final: emit result; break
  for call in out.tool_calls:
      decision = policy.authorize(agent, call)   → allow | deny | needs_approval
      result   = tool_router.invoke(call) in sandbox (timeout, output cap)
      append events; checkpoint
```
- **Stop reasons** are first-class: `completed`, `budget_exhausted`, `denied`, `awaiting_approval`, `error`, `cancelled`.
- **Local permission check** = a fast allow/deny cache for common calls, so the control plane isn't hit on every `read_file`.
- **Hooks**: `pre_tool`, `post_tool`, `pre_model`, `on_stop`, used for guards, redaction and custom logic.
- A pending approval **parks** the run (checkpoint + exit), so nothing blocks waiting on a human.

### 3.3 Tools and integrations
**Job:** let the agent act.
- **MCP is the tool contract.** A tool written in any language is just an MCP server, which is how the multi-language goal is met.
- A **tool router** combines tools from several MCP servers, namespaces them, and attaches a **risk class** to each (`read`, `write-local`, `write-external`, `exec`, `irreversible`). Policies are written against risk classes, not individual tool names.
- Built-in servers for V1: `fs`, `shell`, `git`, `search` (code grep), `http` (allowlisted hosts), `github` (issues/PRs).
- Tool output is **capped and summarized** before it reaches context. The full output goes to the event log.

### 3.4 Context, memory and state
Three separate concerns:

| Store | Lifetime | Contents | V1 backing |
|-------|----------|----------|------------|
| **Working context** | One model call | System prompt, task, plan, recent turns, selected tool results | Built per step |
| **Task state** | One run | Plan, todo list, files touched, checkpoints | Fold over the event log |
| **Memory** | Across runs | Repo facts, conventions, past failures/fixes | SQLite + embeddings, scoped per project |

- A **context manager** with a token budget per section; when over budget, it compacts old turns into summaries.
- Memory writes are **explicit and attributable** (an agent calls `remember`, which is an event); this avoids silent drift and lets bad memories be removed.

### 3.5 Orchestrator
**Job:** task order, dependencies, delegation, multiple agents.
- A **task graph** (DAG) of `Task{ id, agent_role, inputs, depends_on, acceptance_criteria, budget }`.
- Patterns supported: **sequential pipeline**, **fan-out / fan-in**, **supervisor → workers**, **review loop** (producer ↔ critic until the criteria pass or the attempt budget runs out).
- Agents communicate through **artifacts + events**, not shared chat history. This keeps contexts small and handoffs auditable.
- Budgets **nest**: a child task's budget is carved out of its parent's.
- Workflows are declared in YAML/JSON (so they're language-neutral) or built in code via the SDK.

### 3.6 Runtime and sandbox
**Job:** execution environment, recovery, isolation.
```go
type Sandbox interface {
    Create(ctx context.Context, spec SandboxSpec) (Handle, error) // image, cpu/mem, network policy, mounts
    Exec(ctx context.Context, h Handle, cmd []string, timeout time.Duration) (ExecResult, error)
    Snapshot(ctx context.Context, h Handle) (SnapshotRef, error)  // for resume / branch
    Destroy(ctx context.Context, h Handle) error
}
```
- V1 backend: Docker via the Docker Engine API from Go, one container per run, **network off by default** and an egress allowlist per policy.
- The repo is mounted as a working copy; changes leave only as a **diff / PR**, never by pushing directly from the sandbox.
- Recovery: if a run crashes, restore from the last checkpoint event and the matching snapshot.
- Later backends behind the same interface: Kubernetes pods, microVMs.

### 3.7 Control plane
**Job:** central management.

| Concern | V1 design |
|---------|-----------|
| **Identity** | Single local user for now. Each agent version has an ID; each run gets a short-lived credential scoped to that run (so secrets given to tools are per run, even locally) |
| **Policy** | Declarative rules over `(agent, tool risk class, resource, environment)` → allow / deny / needs_approval. Start with a small built-in rule engine; keep the interface open for OPA/Cedar later |
| **Budgets** | Per agent, project and run: tokens, $, wall-clock, tool calls. Harness enforces; control plane accounts |
| **Approvals** | Queue + API + CLI (`adlc approve <id>`); later Slack/web UI |
| **Registry / deployments** | Agent definition = prompt + model policy + tool set + policies, versioned; promote `dev → staging → prod` only after evals pass |
| **Ops controls** | Kill switch (global / agent / run), pause, rate limits |

V1 runs **inside the `adlc` binary** (`adlc serve` for a long-running local daemon, or embedded in `adlc run`). It is exposed as a gRPC API with an HTTP gateway; this is what the generated SDKs talk to.

### 3.8 Observability and evaluation
- **Tracing:** event log → OpenTelemetry spans (run → step → model call / tool call); any OTel backend works.
- **Cost:** tokens and $ per event, rolled up per run, agent, project and model.
- **Failure taxonomy:** stop reasons + error classes (tool error, model error, policy denial, timeout, loop detected).
- **Evaluation**, three kinds:
  1. **Outcome checks** (deterministic): tests pass, build succeeds, linter clean, diff applies.
  2. **Trajectory checks:** step count, wasted calls, policy violations, and regressions against a baseline.
  3. **Model-graded rubrics:** for qualitative output (spec quality, review usefulness), checked against human labels.
- **Eval suites** = sets of tasks with fixtures (repo snapshot + issue + hidden tests). They run in CI on every agent version, and their result **gates promotion** in the registry.
- **Replay:** re-run a recorded run against a new model or prompt to compare.

## 4. SDLC reference workload (built on the platform)

| Stage | Agent role | Key tools | Output artifact | Gate |
|-------|-----------|-----------|-----------------|------|
| Intake | Triage | github, search | Classified issue + scope | — |
| Spec | Spec writer | search, fs(read) | `spec.md` with acceptance criteria | Human approval (optional) |
| Design | Architect | search, fs(read) | Design notes / ADR | Review loop |
| Implement | Coder | fs, shell, git | Diff | Tests pass |
| Test | Tester | fs, shell | New/updated tests + report | Coverage / pass |
| Review | Reviewer | git, search | Review comments | Critic loop with Coder |
| Release | Releaser | github | PR opened | **Human approval required** |

## 5. Repository layout (proposed)

Standard Go module layout:

```
adlc/
  go.mod
  cmd/adlc/                 # single binary: run | serve | approve | eval | replay | kill
  contracts/                # .proto + JSON Schema, the language-neutral source of truth
    proto/adlc/v1/          # events, harness API, control plane API, plugin services
    schema/                 # agent.yaml / workflow.yaml / tool-policy schemas
  gen/go/                   # generated Go code from contracts (committed)
  internal/
    harness/                # agent loop, budgets, stop reasons, hooks
    orchestrator/           # task graph, delegation, review loops
    context/                # context builder, compaction
    memory/                 # cross-run memory store
    eventlog/               # append-only log, fold-to-state, checkpoints
    toolrouter/             # MCP client, namespacing, risk classes
    sandbox/                # Sandbox interface
      docker/               # Docker backend
    controlplane/           # identity, policy engine, budgets, approvals, registry
    observability/          # OTel exporter, cost rollups
    evals/                  # runner, built-in graders
    plugin/                 # launching out-of-process plugins and talking gRPC to them
  adapters/models/          # one Go package per provider (in-process)
  tools/                    # built-in MCP servers written in Go (fs, shell, git, search, github)
  agents/sdlc/              # reference SDLC agents as YAML + prompts
  sdks/                     # generated clients for other languages (M4)
    python/  typescript/
  examples/plugins/         # one sample plugin per language, to prove the boundary works
```

`internal/` keeps the core's internals private. The stable public surface is `contracts/` plus a small `pkg/` (added only when an interface is ready to be promised).

## 6. Milestones

1. **M0 — Contracts & skeleton:** event schema, `ModelAdapter`, `Sandbox`, `PolicyClient`, tool router; a hello-world agent; event log → console trace.
2. **M1 — One agent, one issue (end-to-end):** coder agent in Docker fixes a seeded bug in a fixture repo, tests pass, diff produced; budgets enforced; full trace; 1 eval suite of about 10 tasks.
3. **M2 — Control plane v1:** policies by risk class, approval parking/resume, kill switch, registry with eval-gated promotion.
4. **M3 — Orchestrator:** task graph, review loop, the full SDLC pipeline from §4.
5. **M4 — Other languages:** generated Python/TS SDKs; one sample plugin in another language for each extension point (tool, model adapter, hook, grader), with tests across the language boundary.
6. **Later (only if needed):** multi-user control plane, Postgres, Kubernetes sandbox backend, web UI for approvals and traces.

Note: even in M0, the plugin gRPC contract is defined and **one** non-Go plugin is tested, so the core can't quietly come to depend on Go-only behaviour.

## 7. Risks and open questions

- **Scope:** eight layers is a platform. Mitigation: milestone order is fixed so that each milestone ships something usable.
- **Provider-agnostic lowest common denominator:** normalizing APIs can hide useful provider-specific features. Mitigation: capabilities flags + adapter-specific extensions.
- **Eval data:** the framework is only as trustworthy as its eval suites. Fixture repos with hidden tests must be built early (M1).
- **Go and the agent ecosystem:** fewer ready-made agent/eval libraries exist in Go than in Python. Mitigation: grader and adapter plugins can be written in Python where a library only exists there.
- **Resolved:** single user, local, no compliance constraints for now.
- **Resolved (round 3):** first adapters are **Anthropic** and **OpenAI**; M1 fixture is a **small Go repo**; SQLite driver is **pure Go** (`modernc.org/sqlite`, no cgo).

## 8. Notes for the first two model adapters

**Anthropic adapter** (official Go SDK `github.com/anthropics/anthropic-sdk-go/v2`):
- Default model is set per agent in `agent.yaml`; the adapter always sets `output_config.effort` explicitly rather than relying on the model's default, and leaves adaptive thinking on (some current models reject requests that disable it).
- Forced `tool_choice` (`any` / a specific tool) returns a 400 on some current models. The harness must therefore never depend on forced tool calls; use `auto` + `strict: true` tool schemas, or structured outputs. This holds for every provider, so it becomes a rule of the `ModelAdapter` contract.
- Stream with `client.Messages.NewStreaming` and the SDK's `message.Accumulate(...)`; check `stop_reason` (including `refusal`) before running any tool call.
- Return **all** tool results for a turn in a **single** user message (parallel tool calls).
- Prompt caching: `CacheControl` on the last stable system block. The context manager keeps tools → system → stable history byte-identical, putting volatile content last.
- Token counting via the `count_tokens` endpoint (backs `ModelAdapter.CountTokens`).
- Pass thinking blocks back unchanged; the event log stores them verbatim.

**OpenAI adapter:** the official OpenAI Go SDK. Its exact API surface will be checked against that SDK's docs when it is implemented; the same adapter with a configurable base URL also serves OpenAI-compatible local servers.

**Contract consequence:** `ModelEvent` must carry provider-opaque blobs (e.g. thinking blocks) that the harness stores and replays without interpreting them.
