# Sandbox pause follow-ups — short design

Status: approved by the owner on 2026-11-14.
Follows `docs/design/2026-11-14-sandbox-pause-design.md` (merged in roman-ambinder/agentic-framework#13).
The five items are the follow-ups listed in that PR's Notes.

## Intent

Close the five gaps the whole-branch review left open, so a paused run never
leaves an unrecorded workspace copy, a leaked container or stale snapshot
files behind, and `approve`/`resume` treat the workspace like `run` does.

## Goals and non-goals

- Goals: the five fixes below, each with a test that fails first.
- Non-goals: process checkpoints (still blocked, design §8); any change to the
  event schema; the remaining AX gaps (separate designs).
- Anti-goal: a fix that turns a run that resumes today into one that refuses.

## Approaches

| Item | Chosen | Considered and dropped |
|---|---|---|
| Orphaned tar when `sandbox_suspended` cannot be recorded | Restore the container from the tar at once; if that fails too, the error names the tar and how to extract it | Record the event before suspending and remove it on failure: impossible, the event log is append-only |
| Snapshot files never deleted | Delete `.adlc/snapshots/<run>/` when `finish` handles a run that is no longer waiting for approval, after the workspace was copied out | Delete on every restore: a later failed approve would have nothing to restore from |
| Agent-version mismatch found only after a restore | Compare `a.AgentID()` with the run's `AgentVersion` before `attachSandbox`, in `approve` and `resume`; same `harness.ErrAgentMismatch` | — |
| Kept container leaks when no tool uses `{{sandbox_id}}` | When the agent file has a `sandbox:` section, look up the live container even without the placeholder; none found → continue without a sandbox, as today | Fail when none is found: would refuse runs that resume today (anti-goal) |
| No workspace flags on `approve`/`resume` | Add `--workspace-out` and `--keep-sandbox` with the same text and behaviour as on `adlc run` | — |

## Constraints

- AGENTS.md rules; no new dependencies; no proto change; `RunState` unchanged.
- Messages start with `sandbox: ` like the existing ones.

## Left to the builder

Exact wording of new stdout lines beyond what the plan pins; test helper reuse.
