# Sandbox Pause (files path) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A run that parks for approval saves `/workspace` to the host and gives up its container; `adlc approve` restores it into a fresh container from the same image.

**Architecture:** An optional `sandbox.Suspender` interface (Suspend/Restore) implemented by the Docker backend. The CLI calls Suspend whenever a run ends in `STOP_REASON_AWAITING_APPROVAL` (from `run`, `fork`, `approve`, `resume`) and Restore when it re-attaches a paused run. Two new event kinds record both; the fold accepts them on paused runs and changes no state.

**Tech Stack:** Go 1.26, `github.com/moby/moby/client v0.6.3` (already in go.mod), protobuf via `make proto`, SQLite event log.

**Spec:** `docs/design/2026-11-14-sandbox-pause-design.md`. Scope: the files path only (design tasks P.1–P.3 and P.5, here split into P.5a and P.5b). Process checkpoints (P.4) are deferred (design §8, spike result).

## Global Constraints

- Read `AGENTS.md` first. All its rules apply (errors prefixed with the package name, no `time.Now()` in tested logic, doc comment on every exported identifier, table-driven tests, `t.TempDir()`, no network in tests).
- Base every task branch on `origin/integration` (the code the design refers to lives there; finished tasks merge into `integration`, as the work plan's status table shows). Branch names as given per task. Commit first line: `<task id>: <summary>` (≤ 72 chars).
- Protobuf: only add. New oneof members are `sandbox_suspended = 23` and `sandbox_restored = 24`. Never edit `gen/`; run `make proto`.
- **Do not add fields to `eventlog.RunState`.** `RunState.Digest()` JSON-encodes the whole struct, so any new field changes the digest of every existing run (see the comment on `Event_RunResumed` in `fold.go`). This replaces design §5.3's `LastSuspendedSeq`; the CLI scans events instead (Task 5).
- No new dependencies.
- Docker-only tests use `//go:build docker` and run with `make test-docker`. `make ci` must pass without Docker.
- Messages printed to stdout start with `sandbox: ` like the existing ones in `internal/cli/run_sandbox.go`.

## Review Focus

1. **A run paused before this feature** (container alive, no `sandbox_suspended` in its log) → `adlc approve` must still work through today's live-container lookup. Test: Task 5 `TestApproveRunWithoutSnapshotUsesLiveContainer`.
2. **A run that parks twice** (approve → parks again → approve) → the second approve restores from the *second* snapshot. Test: Task 5 `TestApproveRestoresLatestSnapshot`.
3. **Suspend fails half-way** (tar cannot be written, CopyOut fails) → container kept, nothing appended, the old `sandbox: kept <id> for resume` path prints. Test: Task 5 `TestFinishKeepsContainerWhenSuspendFails`.
4. **Snapshot tar missing or edited on disk** → restore refuses before creating a container, names the file, appends nothing. Test: Task 3 `TestRestoreRejectsChangedTar` and Task 5 `TestApproveFailsWhenSnapshotMissing`.
5. **Workspace with a symlink, an executable file and a file owned by the sandbox user** round-trips byte for byte, mode and owner kept. Test: Task 3 `TestSuspendRestoreRoundTrip` (docker).

---

### Task 1: Event schema and fold (branch `task/P.1-sandbox-events`)

**Files:**
- Modify: `contracts/proto/adlc/v1/events.proto` (oneof + two messages)
- Modify: `contracts/contracts_test.go` (`samplePayloads`: one entry per new kind)
- Modify: `internal/eventlog/fold.go` (status check and payload switch)
- Test: `internal/eventlog/fold_test.go`
- Modify: `docs/impl/interfaces.md` §3 (one paragraph naming the two kinds and the paused-run rule)

**Interfaces:**
- Produces: `adlcv1.SandboxSuspended{SnapshotRef, WorkspaceSha256, WorkspaceBytes, ImageId, CheckpointId, ProcessesSkipped}`, `adlcv1.SandboxRestored{SuspendedSeq, Processes, FallbackCause, SandboxId}`, oneof wrappers `adlcv1.Event_SandboxSuspended`, `adlcv1.Event_SandboxRestored`. Field numbers and comments exactly as design §5.3.

- [ ] **Step 1: Add the two messages and oneof members to `events.proto`, run `make proto`.** Expected: `gen/go/adlc/v1/events.pb.go` regenerated, `make proto-breaking` passes.
- [ ] **Step 2: Write failing fold tests** in `fold_test.go`:
  - `TestPausedRunAcceptsSandboxEvents`: events run_started, run_completed(AWAITING_APPROVAL), sandbox_suspended, sandbox_restored, approval_resolved → `Fold` succeeds; after the two sandbox events `Status == StatusPaused`; after approval_resolved `Status == StatusRunning`; `UnknownEvents == 0`.
  - `TestRunningRunAcceptsSandboxEvents`: run_started, sandbox_restored → succeeds, `StatusRunning`.
  - `TestSandboxEventsChangeNoStateField`: take the state after run_completed(AWAITING), copy it, apply sandbox_suspended to the copy; zero `LastSeq` and `LastEventAt` in both; assert the two are deeply equal (`reflect.DeepEqual`). This guards the digest: a sandbox event may move only the seq and time.
  - `TestPausedRunStillRejectsOtherKinds`: table over model_call_started, tool_call_requested, checkpoint after AWAITING_APPROVAL → each fails with "paused run can only continue with approval_resolved" (update that message to "paused run can only continue with approval_resolved or a sandbox event" and assert the new text).
  - `TestFinishedRunRejectsSandboxEvents`: run_completed(COMPLETED) then sandbox_suspended → error "run already finished".
- [ ] **Step 3: Run** `go test ./internal/eventlog/ -run 'Sandbox|Paused' -v`. Expected: FAIL (sandbox events counted as unknown / rejected while paused).
- [ ] **Step 4: Implement in `fold.go`:** in the `StatusPaused` case, let `GetSandboxSuspended() != nil || GetSandboxRestored() != nil` through without changing `Status`; add `case *adlcv1.Event_SandboxSuspended, *adlcv1.Event_SandboxRestored:` that changes nothing, with the same comment style as `Event_RunResumed`.
- [ ] **Step 5: Add both kinds to `samplePayloads` in `contracts_test.go`.** Run `make test`. Expected: PASS, including `TestEveryPayloadKindCovered`.
- [ ] **Step 6: Break it on purpose** (remove the paused-state exception), see `TestPausedRunAcceptsSandboxEvents` fail, restore. Update `interfaces.md` §3. Run `make ci`. Expected: PASS.
- [ ] **Step 7: Commit** `P.1: sandbox_suspended and sandbox_restored events`.

### Task 2: `sandbox.Suspender` interface and fake (branch `task/P.2-suspender`)

**Files:**
- Modify: `internal/sandbox/sandbox.go`
- Create: `internal/sandbox/sandboxtest/pausable.go`
- Test: `internal/sandbox/sandboxtest/pausable_test.go`
- Modify: `docs/impl/interfaces.md` §8

**Interfaces:**
- Consumes: nothing new.
- Produces (in `internal/sandbox`):
  ```go
  type Suspender interface {
      Suspend(ctx context.Context, h Handle, tarPath string) (Snapshot, error)
      Restore(ctx context.Context, spec Spec, snap Snapshot) (Handle, error)
  }
  type Snapshot struct {
      TarPath          string // absolute path of the /workspace archive
      SHA256           string // "sha256:<hex>" of the file at TarPath
      Bytes            int64
      ImageID          string // image ID the container ran, e.g. "sha256:…"
      ProcessesSkipped string // why processes were not kept
  }
  var ErrSnapshotChanged = errors.New("sandbox: snapshot file changed")
  var ErrImageGone       = errors.New("sandbox: snapshot image is gone")
  const ProcessesNotSupported = "process checkpoints are not supported yet"
  ```
  (Design §5.1's `SuspendOptions`, `CheckpointID` and `RestoreResult` are left out until P.4: YAGNI. `Restore` returning no result means "files only".)
- Produces (in `sandboxtest`): `type Pausable struct{ … }` implementing `sandbox.Sandbox` and `sandbox.Suspender` in memory: `Create` stores the spec and returns `Handle{ID: "fake-<n>"}`; `Suspend` writes the bytes set in field `Workspace []byte` to `tarPath`, fills `Snapshot` (SHA-256 of the bytes, `ImageID: "sha256:fake-" + spec.Image`, `ProcessesSkipped: sandbox.ProcessesNotSupported`) and marks the handle destroyed; `Restore` re-hashes `snap.TarPath` (mismatch → `ErrSnapshotChanged`), checks `snap.ImageID` is in field `Images map[string]bool` when that map is non-nil (missing → `ErrImageGone`), then acts like `Create`. Exported fields `SuspendErr`, `RestoreErr error` force failures. Method `Live() []sandbox.Handle` lists handles not destroyed.

- [ ] **Step 1: Write failing tests** in `pausable_test.go`: `TestPausableSuspendWritesTarAndDestroys` (tar file content == `Workspace`, `Live()` empty, snapshot SHA matches), `TestPausableRestoreRejectsChangedTar` (overwrite file → `errors.Is(err, sandbox.ErrSnapshotChanged)`, `Live()` empty), `TestPausableRestoreRejectsMissingImage` (`Images: map[string]bool{}` → `ErrImageGone`).
- [ ] **Step 2: Run** `go test ./internal/sandbox/... -run Pausable -v`. Expected: FAIL (undefined).
- [ ] **Step 3: Add the types to `sandbox.go` and implement `Pausable`.** Add `var _ sandbox.Suspender = (*Pausable)(nil)`.
- [ ] **Step 4: Run the tests.** Expected: PASS. Break the SHA check, see the changed-tar test fail, restore.
- [ ] **Step 5: Update `interfaces.md` §8, run `make ci`, commit** `P.2: sandbox.Suspender interface and in-memory fake`.

### Task 3: Docker Suspend and Restore (branch `task/P.3-docker-suspend`)

**Files:**
- Create: `internal/sandbox/docker/suspend.go`
- Test: `internal/sandbox/docker/suspend_test.go` (no tag: pure helpers), `internal/sandbox/docker/suspend_integration_test.go` (`//go:build docker`)

**Interfaces:**
- Consumes: Task 2's `sandbox.Suspender`, `Snapshot`, `ErrSnapshotChanged`, `ErrImageGone`, `ProcessesNotSupported`.
- Produces: `func (b *Backend) Suspend(ctx context.Context, h sandbox.Handle, tarPath string) (sandbox.Snapshot, error)` and `func (b *Backend) Restore(ctx context.Context, spec sandbox.Spec, snap sandbox.Snapshot) (sandbox.Handle, error)`; `var _ sandbox.Suspender = (*Backend)(nil)`. Helper `func fileSHA256(path string) (string, int64, error)` returning `"sha256:<hex>"`.

Behaviour (design §5.2, files path):
- **Suspend:** `CopyOut(h, sandbox.WorkspacePath)`; write it to `tarPath` via a temp file in the same directory + `os.Rename` (create parent dirs, mode 0o600); `ContainerInspect` → `.Image` as `ImageID`; then `Destroy(h)`. Any error before `Destroy` returns the error with the container untouched and no file left at `tarPath`.
- **Restore:** `fileSHA256(snap.TarPath)` must equal `snap.SHA256` else `fmt.Errorf("docker: restore %s: %w", snap.TarPath, sandbox.ErrSnapshotChanged)`; `ImageInspect(snap.ImageID)` not found → `fmt.Errorf("docker: restore: image %s: %w", snap.ImageID, sandbox.ErrImageGone)`; then `Create` with a copy of `spec` where `Image = snap.ImageID` and `WorkspaceDir = ""`; then `CopyToContainer(DestinationPath "/", Content = the tar file, CopyUIDGID true)`; on copy failure `forceRemove` and return the error. Do not pass the tar through `sandbox.WorkspaceTar` (it drops symlinks and resets owners).

- [ ] **Step 1: Write failing unit test** `TestFileSHA256` (known bytes → known hex, missing file → error) in `suspend_test.go`.
- [ ] **Step 2: Write failing docker tests** in `suspend_integration_test.go`, image `busybox` like the existing integration tests:
  - `TestSuspendRestoreRoundTrip`: create; `Exec` `sh -c 'echo hi > /workspace/a.txt && printf "#!/bin/sh\n" > /workspace/run.sh && chmod 755 /workspace/run.sh && ln -s a.txt /workspace/link'`; Suspend to `t.TempDir()/1.tar`; assert `FindByRunID` returns none; Restore; `Exec` `sh -c 'cat /workspace/link; stat -c "%a %u" /workspace/run.sh; readlink /workspace/link'` → stdout `"hi\n755 65532\na.txt\n"`.
  - `TestRestoreRejectsChangedTar`: Suspend, append one byte to the tar, Restore → `errors.Is(err, sandbox.ErrSnapshotChanged)` and `FindByRunID` returns none.
  - `TestRestoreRejectsMissingImage`: Suspend, set `snap.ImageID = "sha256:" + strings.Repeat("0", 64)`, Restore → `errors.Is(err, sandbox.ErrImageGone)`.
  - `TestSuspendKeepsContainerWhenTarPathUnwritable`: tarPath under a file (not a dir) → error; the container still answers `Exec("true")`.
- [ ] **Step 3: Run** `go test ./internal/sandbox/docker/ -run FileSHA256 -v` and `go test -tags docker ./internal/sandbox/docker/ -run 'Suspend|Restore' -v`. Expected: FAIL (undefined).
- [ ] **Step 4: Implement `suspend.go`.**
- [ ] **Step 5: Run both commands.** Expected: PASS. Break the owner flag (`CopyUIDGID: false`), see the round trip fail on the owner, restore.
- [ ] **Step 6: Run** `make ci` and `make test-docker`. Expected: PASS. **Commit** `P.3: Docker backend saves and restores /workspace`.

### Task 4: `sandbox.pause` in the agent file (branch `task/P.5a-pause-field`)

**Files:**
- Modify: `internal/agentconfig/agentconfig.go` (`Sandbox` struct, defaults), `internal/agentconfig/validate.go`
- Test: `internal/agentconfig/sandbox_test.go`
- Modify: `docs/impl/interfaces.md` §7

**Interfaces:**
- Produces: field `Pause string \`yaml:"pause"\`` on `agentconfig.Sandbox`; constants `PauseAuto = "auto"`, `PauseFiles = "files"`, `PauseKeep = "keep"`; default `PauseAuto` applied where `DefaultSandboxImage` is applied today; method `func (s *Sandbox) Suspends() bool` returning `s.Pause != PauseKeep`. (`auto` and `files` behave the same until P.4.)

- [ ] **Step 1: Write failing table test** `TestSandboxPause`: `""` → `auto`, `Suspends()` true; `files` → true; `keep` → false; `later` → `Validate` error containing `sandbox.pause must be auto, files or keep`.
- [ ] **Step 2: Run** `go test ./internal/agentconfig/ -run TestSandboxPause -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected: PASS. `make ci`. **Commit** `P.5a: sandbox.pause agent field`.

### Task 5: CLI wiring (branch `task/P.5b-cli-pause`, after Tasks 1–4 are merged)

**Files:**
- Modify: `internal/cli/run_sandbox.go` (`runBackend`, `finish`), `internal/cli/resume.go` (`sandboxLookup`, `attachSandbox`), `internal/cli/approve.go` (`continueRun`), `internal/cli/run.go`, `internal/cli/fork.go` (pass the store and db path to `finish`)
- Create: `internal/cli/snapshot.go`
- Test: `internal/cli/snapshot_test.go`, `internal/cli/run_sandbox_test.go`, `internal/cli/approve_test.go`
- Modify: `docs/design/2026-11-14-sandbox-pause-design.md` (§5.3: replace `LastSuspendedSeq` with "the CLI scans the log", citing the digest constraint), `docs/plans/2026-11-13-adlc-work-plan.md` (status row "P.1–P.3, P.5a–P.5b sandbox pause, files path"), `docs/impl/interfaces.md` §11

**Interfaces:**
- Consumes: Task 1 event types; Task 2 `sandbox.Suspender`, `Snapshot`, `sandboxtest.Pausable`; Task 4 `(*agentconfig.Sandbox).Suspends()`.
- Produces (in `snapshot.go`):
  - `func snapshotPath(dbPath, runID string, seq uint64) string` → `filepath.Join(filepath.Dir(dbPath), "snapshots", runID, fmt.Sprintf("%d.tar", seq))`.
  - `func pendingSnapshot(ctx context.Context, store *eventlog.Store, runID string) (snap sandbox.Snapshot, seq uint64, ok bool, err error)`: reads all events; the last `sandbox_suspended` with no later `sandbox_restored` whose `suspended_seq` equals its seq → `ok`. `SnapshotRef` is stored relative to `filepath.Dir(dbPath)`; return it as an absolute `TarPath`.
  - `func appendSuspended(ctx, store, runID string, snap sandbox.Snapshot, dbDir string) error` and `func appendRestored(ctx, store, runID string, suspendedSeq uint64, snap sandbox.Snapshot, h sandbox.Handle) error` (set `FallbackCause` to `snap.ProcessesSkipped`, `Processes: false`).
- `runBackend` and `sandboxLookup` both gain `sandbox.Suspender` methods; `*docker.Backend` already satisfies them after Task 3. `attachSandbox` returns `(*runSandbox, error)` so `continueRun` and `resumeAgent` can call `finish`.

Behaviour:
- `finish` with `STOP_REASON_AWAITING_APPROVAL` and `Suspends()`: next seq = `state.LastSeq + 1`; `Suspend(h, snapshotPath(db, runID, nextSeq))`; on success `appendSuspended` and print `sandbox: paused (files only: <ProcessesSkipped>)`; on any error print the error and fall back to `keepForResume` (append nothing). With `pause: keep`: `keepForResume` as today.
- `attachSandbox`: if `pendingSnapshot` says `ok`, `Restore(a.SandboxSpec(runID), snap)`, `appendRestored`, expand tool commands with the new handle, print `sandbox: restored <short id> (files only)`; on error return `fmt.Errorf("resume: restore sandbox from %s: %w", snap.TarPath, err)`. Otherwise today's live lookup, unchanged.
- `continueRun` / `resumeAgent`: after `harness.Approve` / `harness.Resume` returns a state, call `finish`. If `harness.Approve` returns an error, read `store.State` and call `finish` with it when its status is `StatusPaused` (design §5.4, "Approve fails after a restore").

- [ ] **Step 1: Write failing tests** with `sandboxtest.Pausable` as the backend:
  - `TestPendingSnapshot` (table): no events → !ok; one suspended → ok, seq; suspended+restored → !ok; suspended, restored, suspended → ok with the second seq.
  - `TestRunSandboxPausedWhenAwaitingApproval` (rename/replace `TestRunSandboxKeptWhenAwaitingApproval`): stdout contains `sandbox: paused (files only: process checkpoints are not supported yet)`; tar exists at `snapshotPath`; log has one `sandbox_suspended`; `Live()` empty.
  - `TestRunSandboxKeepPauseKeepsContainer`: `pause: keep` → old message `sandbox: kept`, no event, `Live()` has one.
  - `TestFinishKeepsContainerWhenSuspendFails`: `SuspendErr` set → stdout has `sandbox: kept`, no `sandbox_suspended` event.
  - `TestApproveRestoresLatestSnapshot`: run parks, approve (fake script parks again), approve again → second restore used the second `sandbox_suspended` seq (check `sandbox_restored.suspended_seq`).
  - `TestApproveRunWithoutSnapshotUsesLiveContainer`: log with a paused run and no sandbox events, live handle in the fake → approve succeeds, no `sandbox_restored`.
  - `TestApproveFailsWhenSnapshotMissing`: delete the tar → approve exits 1, error contains the tar path, log unchanged after the failed attempt, `Live()` empty.
- [ ] **Step 2: Run** `go test ./internal/cli/ -run 'Snapshot|Paused|Pause|Approve' -v`. Expected: FAIL.
- [ ] **Step 3: Implement `snapshot.go`, then the `finish` / `attachSandbox` / `continueRun` / `resumeAgent` changes.**
- [ ] **Step 4: Run the tests.** Expected: PASS. Break `pendingSnapshot` (take the first instead of the last), see `TestApproveRestoresLatestSnapshot` fail, restore.
- [ ] **Step 5: Manual demo on a Docker host** (`make test-docker` must also pass): run `examples/sandbox-hello.yaml` with a fake script that requests approval, check `docker ps -a --filter label=adlc.run_id=<run>` is empty and `.adlc/snapshots/<run>/` has a tar, then `adlc approve`, check the run completes. Paste the commands and output into the PR's `## Testing`.
- [ ] **Step 6: Update the three docs listed above, run `make ci`, commit** `P.5b: pause and restore the sandbox of a parked run`.
