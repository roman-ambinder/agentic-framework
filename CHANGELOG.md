# Changelog

All notable changes. Versions follow the work plan (§7): `v0.1.0` after M0,
`v0.2.0` after M1.

## Unreleased — M2-C (control plane) and M2-Q (agent quality)

C.1–C.4 and Q.1–Q.2 are on `main` (PR #11); Q.3–Q.5 are on `integration`.

### Control plane (C.1–C.4)
- `adlc approvals` lists runs waiting for an approval; `adlc approve` answers one (`--reject`, `--comment`, `--by`) and continues the run in place when no approval is left (C.1).
- Policy rules match tool-name globs and arguments (glob, prefix, regex, list of values); agent files include shared policy files with `policy.include`; `adlc policy check` shows which rule decides a call (C.2).
- Control database `.adlc/control.db`; `adlc kill run|agent`, `adlc unkill`, `adlc kills`. A killed run stops before its next model or tool call, including an approved call (C.3).
- Agent registry: `adlc agent register|list|show|promote`; versions are immutable and content-addressed; promotion to staging or prod needs an eval report for that exact version that meets the pass@1 and cost thresholds; `run`, `resume` and `approve` accept `NAME@N` and `NAME@ENV` (C.4).

### Evals (Q.1, Q.2)
- `adlc eval --repeat N`, pass@k, `adlc eval-compare`; `adlc eval-report --failures` names the top failure causes.

### Agent quality (Q.3–Q.5)
- `adlc fork RUN_ID AGENT --at-step N` continues a run's transcript from a checkpoint with another agent; `run_started` records `forked_from_run_id` and `forked_at_step` (Q.3).
- Optional `planning:` section: one tool-less planning call before step 1, kept in every later request; counts toward token and cost budgets, not max_steps; off by default (Q.4).
- `model.roles.plan` gives the planning call its own provider, model, effort and pricing (Q.5).

## Unreleased — proposed v0.2.0

Everything below is merged on `main`. Gates G0 (a real model through the
loop) and G1 (live eval results) have not been run yet; tag once they have,
or tag `v0.2.0-rc.1` now.

### Agent loop and state (M0)
- Append-only SQLite event log with a validated fold to run state and a stable state digest (0.3).
- Harness: model/tool loop, stop reasons, budgets (steps, tokens, wall clock, cost, tool calls), retries with backoff, pre/post tool hooks, per-step checkpoints (0.5.1–0.5.4).
- Resume a crashed run from its last checkpoint, verified by a test that kills the process at every event boundary (1.7).
- Context manager: token budgets, elision of old tool output, stable request prefixes for prompt caching (1.6.1, 1.6.2).

### Models
- Provider-neutral model types, scripted fake model and an adapter contract suite (0.4).
- Anthropic adapter with streaming, prompt caching and token counting (0.5b, 1.1); OpenAI adapter (1.2); router with fallback on retryable errors (1.3).
- Model ids and prices come from environment variables; none are committed.

### Tools, policy and sandbox
- Static policy rules on risk classes (0.6); tool router and MCP router (0.7.1, 0.7.2); file tools confined with `os.Root` (0.8).
- Docker sandbox: no network by default, CPU/memory/process limits, non-root user, workspace copied in and out (1.4.1, 1.4.2).
- Shell, git, search and file tools that run inside the sandbox (1.5).
- Out-of-process hook plugins over gRPC, with a Python example (0.11.1, 0.11.2).

### Command line
- `adlc run`, `trace`, `cost`, `resume`, `eval`, `eval-report`, `tool`, `version` (0.9.1, 0.9.2, 0.10, 1.11, 1.7, 1.8, 1.9).
- `adlc eval --dry-run` reports the worst-case spend before anything runs.

### Evals
- Ledger fixture project and 10 seeded bugs with hidden tests, checked in CI (E.1–E.3).
- Eval runner and graders: hidden tests, build, vet and existing tests in a fresh sandbox; pass@1 per difficulty and provider (1.8, 1.9).
- Coder agent files and a manual GitHub Actions workflow for live evals with a spend limit (1.10, offline part).

### Security
- Changed files after an eval run are computed with the host-side git directory, so a `.git/config` planted by an agent cannot run programs on the host.

### Build
- Go 1.26 (toolchain go1.26.11); all developer tools pinned in `tooling/go.mod` (0.0).
- CI: tests with the race detector, lint, protobuf compatibility, Docker tests, Python plugin test and the seeded-bug self-check.
