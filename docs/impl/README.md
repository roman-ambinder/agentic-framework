# Implementation briefs

Each task in the work plan (`docs/plans/2026-11-13-adlc-work-plan.md`) has a
brief in `tasks/`. A brief is written so that an implementer who has not seen
the design discussion can finish the task without making design decisions.

## How to use a brief

1. Read `AGENTS.md` (repo root).
2. Read `interfaces.md` (this folder): the exact shared Go types.
3. Read the brief, start to finish, before writing code.
4. Read every file listed under "Read first".
5. Follow "Steps" in order. Run the "Verify" commands.
6. Tick the "Done checklist", open a PR as described in `AGENTS.md`.

If anything in a brief is unclear or wrong, stop and report it (see
"When to stop and ask" in `AGENTS.md`).

`interfaces.md` already contains the reconciled API of every task. When a brief
says "add X to `interfaces.md`", check whether X is already there with the same
signature; if it is, leave it and say so in the PR. If it differs, stop and report.

## Task index and order

Sizes: S = small, M = medium, L = large (see the work plan, §1).
A task can start once everything in "Depends on" is merged.

### M0 — walking skeleton

| ID | Brief | Size | Depends on |
|----|-------|------|-----------|
| 0.0 | [Upgrade to Go 1.26](tasks/0.0-go-upgrade.md) | S | — |
| 0.4 | [Model types, adapter interface, fake model](tasks/0.4-model.md) | M | 0.0 |
| 0.5.1 | [Harness core loop](tasks/0.5.1-harness-loop.md) | M | 0.4, 0.6, 0.7.1 |
| 0.5.2 | [Harness budgets](tasks/0.5.2-harness-budgets.md) | S | 0.5.1 |
| 0.5.3 | [Harness retries](tasks/0.5.3-harness-retries.md) | S | 0.5.1 |
| 0.5.4 | [Harness hooks and checkpoints](tasks/0.5.4-harness-hooks-checkpoints.md) | S | 0.5.1 |
| 0.5b | [Anthropic adapter, minimal](tasks/0.5b-anthropic-minimal.md) | M | 0.4 |
| 0.6 | [Policy client, static rules](tasks/0.6-policy.md) | S | 0.4 |
| 0.7.1 | [Tool router interface, static router, output cap](tasks/0.7.1-toolrouter-core.md) | S | 0.4 |
| 0.7.2 | [MCP tool router](tasks/0.7.2-toolrouter-mcp.md) | M | 0.7.1 |
| 0.8 | [fs MCP server](tasks/0.8-tools-fs.md) | M | 0.7.2 |
| 0.9.1 | [agent.yaml schema and loader](tasks/0.9.1-agentconfig.md) | M | 0.4, 0.6 |
| 0.9.2 | [`adlc run` command](tasks/0.9.2-cli-run.md) | M | 0.5.4, 0.5b, 0.8, 0.9.1 |
| 0.10 | [Trace export and `adlc trace`](tasks/0.10-trace.md) | M | 0.9.2 |
| 0.11.1 | [Plugin contract and Go launcher](tasks/0.11.1-plugin-launcher.md) | M | 0.5.4 |
| 0.11.2 | [Python hook plugin](tasks/0.11.2-plugin-python.md) | M | 0.11.1 |

### Eval track (alongside M0)

| ID | Brief | Size | Depends on |
|----|-------|------|-----------|
| E.1 | [Fixture repository](tasks/E.1-fixture-repo.md) | M | 0.0 |
| E.2 | [Seeded bugs](tasks/E.2-seeded-bugs.md) | L | E.1 |
| E.3 | [Fixture format and loader](tasks/E.3-fixture-loader.md) | S | E.2 |
| E.4 | [Manual baseline](tasks/E.4-baseline.md) | S | E.3 |

### M1 — one agent fixes one issue

| ID | Brief | Size | Depends on |
|----|-------|------|-----------|
| 1.1 | [Anthropic adapter: streaming, caching, token counting](tasks/1.1-anthropic-full.md) | M | 0.5b |
| 1.2 | [OpenAI adapter](tasks/1.2-openai.md) | M | 0.4, 0.9.2 |
| 1.3 | [Model router and fallback](tasks/1.3-model-router.md) | S | 1.1, 1.2 |
| 1.4.1 | [Sandbox interface and Docker backend](tasks/1.4.1-sandbox-docker.md) | M | 0.0 |
| 1.4.2 | [Sandbox limits, network, workspace](tasks/1.4.2-sandbox-isolation.md) | M | 1.4.1 |
| 1.5 | [shell, git, search tools in the sandbox](tasks/1.5-sandbox-tools.md) | M | 0.7.2, 1.4.2 |
| 1.6.1 | [Context manager: budgets and compaction](tasks/1.6.1-context-budgets.md) | M | 0.5.1 |
| 1.6.2 | [Context manager: stable prefix for caching](tasks/1.6.2-context-caching.md) | S | 1.6.1, 1.1 |
| 1.7 | [Resume a crashed run](tasks/1.7-resume.md) | M | 0.5.4, 1.4.2, 0.9.2 |
| 1.8 | [Eval runner](tasks/1.8-eval-runner.md) | M | E.3, 1.5, 0.9.2 |
| 1.9 | [Graders and report](tasks/1.9-graders.md) | M | 1.8 |
| 1.10 | [Coder agent](tasks/1.10-coder-agent.md) | L | 1.9 |
| 1.11 | [`adlc cost`](tasks/1.11-cost.md) | S | 0.9.2 |

### G — gaps found while building M0/M1 (independent of gate G1)

| ID | Brief | Size | Depends on |
|----|-------|------|-----------|
| G.1 | [Sandbox for `adlc run`](tasks/G-gaps.md#g1--sandbox-for-adlc-run) | M | — |
| G.2 | [Hook plugins in the agent file](tasks/G-gaps.md#g2--hook-plugins-declared-in-the-agent-file) | M | — |
| G.3 | [CLI consistency](tasks/G-gaps.md#g3--cli-consistency) | S | — |
| G.4 | [Context manager memory keyed by position](tasks/G-gaps.md#g4--context-manager-memory-keyed-by-position) | S | — |

M2 (both options, briefed before gate G1): see [m2.md](m2.md). M3 and later are planned after M2.

## Brief template

Every brief uses these sections, in this order:

```
# <ID> — <title>
Size · Depends on · Branch · PR title

## Goal              one paragraph: what exists after this task
## Read first        files to read before starting
## Files             files to create or change
## Design            exact types, signatures, algorithms, formats
## Steps             numbered, each small enough to check
## Tests             test names and what each one asserts
## Verify            commands to run and what you should see
## Out of scope      what not to do (later tasks do it)
## Pitfalls          mistakes that are easy to make here
## Done checklist
```
