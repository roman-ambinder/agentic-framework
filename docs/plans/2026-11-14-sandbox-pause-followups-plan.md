# Sandbox Pause Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the five follow-ups left by roman-ambinder/agentic-framework#13 in the CLI's sandbox pause handling.

**Architecture:** All changes are in `internal/cli` (run_sandbox.go, snapshot.go, resume.go, approve.go) and their tests, using the existing `sandboxtest.Pausable` fake.

**Tech Stack:** Go 1.26, cobra, the existing event log and sandbox packages.

**Spec:** `docs/design/2026-11-14-sandbox-pause-followups-design.md` (and the parent `docs/design/2026-11-14-sandbox-pause-design.md`).

## Global Constraints

- Read `AGENTS.md`; all its rules apply. No new dependencies, no proto change, no new `eventlog.RunState` field.
- Work on branch `claude/wizardly-pascal-gylbmh` (restarted from `origin/integration` at `67f8fc0`). Commit subjects `F.<n>: <summary>` (≤ 72 chars), body bullets. No AI attribution; keep the configured git identity.
- Stdout lines start with `sandbox: `.
- Each fix starts with a test that fails on the current code.
- `make ci` must pass; run `make test-docker` once at the end of Task 5 (a Docker daemon is running).

## Review Focus

1. A run whose `sandbox_suspended` could not be recorded must end with a live container (or, if restore fails too, an error naming the tar) — never with neither. Test: Task 1.
2. A failed `adlc approve` after a run finished must not have deleted the snapshot it may still need — deletion happens only for a run that is not waiting for approval. Test: Task 2 `TestFinishKeepsSnapshotsWhileAwaitingApproval`.
3. A version mismatch must refuse before any container is created or any event appended. Test: Task 3.
4. An agent with a `sandbox:` section and no placeholder whose container is gone must still resume without a sandbox, as today. Test: Task 4 `TestAttachSandboxSpecWithoutPlaceholderNoContainer`.
5. `--keep-sandbox` on `approve` keeps the container after completion. Test: Task 5.

---

### Task 1: Restore when the pause cannot be recorded (F.1)

**Files:** Modify `internal/cli/run_sandbox.go` (`runSandbox` gains `spec sandbox.Spec`, set in `startRunSandbox` and `attachSandbox`), `internal/cli/snapshot.go` (`suspend`); Test `internal/cli/run_sandbox_test.go`.

**Interfaces:** Produces field `runSandbox.spec sandbox.Spec`. Uses `runBackend.Restore`.

Behaviour of `suspend` when `appendSuspended` fails after a successful Suspend:
- `h, rerr := s.backend.Restore(bg, s.spec, snap)`. On success: `s.handle = h`, print `sandbox: pause not recorded (<append error>); restored <short id> and kept it for resume`, return nil (same exit behaviour as a failed Suspend today).
- On restore failure: return `fmt.Errorf("run: sandbox saved to %s but not recorded (%v) and not restored: %w; recover the files with: tar -xf %s", snap.TarPath, appendErr, rerr, snap.TarPath)`.

- [ ] **Step 1:** Write `TestSuspendRestoresWhenRecordFails` (the store is closed before `finish`, or use a store wrapper that fails Append — pick what the existing tests make easy; assert `Live()` has one handle, stdout contains `sandbox: pause not recorded`, no `sandbox_suspended` event) and `TestSuspendNamesTarWhenRecordAndRestoreFail` (`RestoreErr` set too; error contains the tar path and `tar -xf`).
- [ ] **Step 2:** Run `go test ./internal/cli/ -run 'TestSuspend(Restores|NamesTar)' -v`. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run. Expected: PASS. Break the restore call, see the first test fail, restore.
- [ ] **Step 5:** `make ci`; commit `F.1: restore the sandbox when its pause cannot be recorded`.

### Task 2: Delete a finished run's snapshots (F.2)

**Files:** Modify `internal/cli/run_sandbox.go` (`finish`), `internal/cli/snapshot.go` (new `func removeSnapshots(dbPath, runID string) error` → `os.RemoveAll(filepath.Join(filepath.Dir(dbPath), "snapshots", runID))`); Test `internal/cli/run_sandbox_test.go`.

Behaviour: in `finish`, for a state that is not `STOP_REASON_AWAITING_APPROVAL`, after the workspace copy-out succeeded, call `removeSnapshots(o.db, s.runID)`; on error print `sandbox: could not remove snapshots: <err>` and continue. If the copy-out failed, keep the snapshots.

- [ ] **Step 1:** Write `TestFinishRemovesSnapshotsWhenRunCompletes` (park, approve to completion → `.adlc/snapshots/<run>` gone), `TestFinishKeepsSnapshotsWhileAwaitingApproval` (park twice → directory and both tars present), `TestFinishKeepsSnapshotsWhenCopyOutFails`.
- [ ] **Step 2:** Run `go test ./internal/cli/ -run 'TestFinish(Removes|Keeps)Snapshots' -v`. Expected: the Removes test FAILS.
- [ ] **Step 3:** Implement. **Step 4:** Run → PASS; break once, restore. **Step 5:** `make ci`; commit `F.2: delete a finished run's snapshot files`.

### Task 3: Check the agent version before restoring (F.3)

**Files:** Modify `internal/cli/approve.go` (`continueRun`), `internal/cli/resume.go` (`resumeAgent`, next to `checkResumable`); Create helper in `internal/cli/resume.go`: `func checkAgentVersion(ctx context.Context, store *eventlog.Store, runID string, a *agentconfig.Agent) error` returning `fmt.Errorf("%w: run has %s, agent file is %s", harness.ErrAgentMismatch, state.AgentVersion, a.AgentID())` on mismatch. Test `internal/cli/approve_test.go`, `internal/cli/resume_test.go`.

Call it after `loadAgent` and before `attachSandbox` in both commands.

- [ ] **Step 1:** Write `TestApproveVersionMismatchRefusesBeforeRestore` (park with agent v1, approve with v2 → `errors.Is(err, harness.ErrAgentMismatch)`, no `sandbox_restored`, no new `sandbox_suspended`, `Live()` empty, tar still present) and the same for resume if a resume test can reach a sandboxed crashed run cheaply; otherwise unit-test `checkAgentVersion` directly (table: same → nil, different → ErrAgentMismatch).
- [ ] **Step 2:** Run `go test ./internal/cli/ -run 'VersionMismatch|CheckAgentVersion' -v`. Expected: FAIL.
- [ ] **Step 3:** Implement. **Step 4:** PASS; break once, restore. **Step 5:** `make ci`; commit `F.3: check the agent version before restoring a sandbox`.

### Task 4: Find the live container without the placeholder (F.4)

**Files:** Modify `internal/cli/resume.go` (`attachSandbox`); Test `internal/cli/resume_test.go`.

Behaviour: when `hasSpec` is true, no snapshot is pending and no tool uses the placeholder, call `FindByRunID`. Exactly one found and the `true` probe answers → attach it (so `finish` copies out and destroys it); none found → return `nil, nil` (today's behaviour); more than one → today's error. With the placeholder, behaviour is unchanged (`findLiveSandbox`, which errors when none is found).

- [ ] **Step 1:** Write `TestAttachSandboxSpecWithoutPlaceholderFindsContainer` (returns non-nil runSandbox with the live handle) and `TestAttachSandboxSpecWithoutPlaceholderNoContainer` (returns nil, nil).
- [ ] **Step 2:** Run `go test ./internal/cli/ -run TestAttachSandboxSpecWithoutPlaceholder -v`. Expected: the first FAILS.
- [ ] **Step 3:** Implement. **Step 4:** PASS; break once, restore. **Step 5:** `make ci`; commit `F.4: attach a kept sandbox even when no tool names it`.

### Task 5: Workspace flags on approve and resume (F.5)

**Files:** Modify `internal/cli/approve.go` and `internal/cli/resume.go` (flag registration); Test `internal/cli/approve_test.go`, `internal/cli/resume_test.go`; update `docs/impl/interfaces.md` §11 with one line.

Register on both commands, with the exact help text `adlc run` uses (`internal/cli/run.go`):
- `--workspace-out` → `o.workspaceOut`: "where to copy the sandbox workspace after the run (default .adlc/workspaces/<run-id>)"
- `--keep-sandbox` → `o.keepSandbox`: "do not destroy the sandbox container after the run"

- [ ] **Step 1:** Write `TestApproveWorkspaceOutFlag` (completed run's files land in the given dir) and `TestApproveKeepSandboxFlag` (container still live after completion, stdout `sandbox: kept`), plus `TestResumeHasWorkspaceFlags` (both flags registered).
- [ ] **Step 2:** Run `go test ./internal/cli/ -run 'WorkspaceOutFlag|KeepSandboxFlag|ResumeHasWorkspaceFlags' -v`. Expected: FAIL (unknown flag).
- [ ] **Step 3:** Implement. **Step 4:** PASS; break once, restore.
- [ ] **Step 5:** `make ci` and `make test-docker`; mark the five follow-ups done in `docs/plans/2026-11-13-adlc-work-plan.md` status table (one row "F.1–F.5 sandbox pause follow-ups"); commit `F.5: --workspace-out and --keep-sandbox on approve and resume`.
