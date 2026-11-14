# Sandbox pause and restore — design

Status: draft for review. Nothing here is built yet.
Code references are to the `integration` branch as of 2026-11-14.

## 1. Intent

A run that waits for a human (`STOP_REASON_AWAITING_APPROVAL`) today keeps its
Docker container running for as long as the wait lasts
(`internal/cli/run_sandbox.go`, `finish`: "A run that awaits approval keeps
its container"). If that container disappears — Docker restarted, host
rebooted, someone ran `docker rm` — the run is lost:
`adlc approve` and `adlc resume` refuse with "sandbox for run %q is not
running; its workspace changes cannot be recovered. Start a new run."

This design lets a paused run give up its container and come back later with
its state intact. It is the idea at the centre of Google's AX ("When an agent
enters an idle state ... the platform checkpoints its execution state and
suspends it"), scaled down to adlc's single-user, single-host setup.

The owner decided that **running processes, not only files, should survive a
pause where the host allows it** (approach A below).

## 2. Goals, non-goals, anti-goals

**Goals**
- G1. When a run parks for approval, its `/workspace` is saved to the host and
  the container stops using CPU and memory.
- G2. When the host supports it (experimental Docker + CRIU), the processes
  inside the container are checkpointed too, and come back running on restore.
- G3. `adlc approve` (and later `adlc resume`) brings a paused run back:
  from the process checkpoint if it is usable, otherwise from the saved files
  in a fresh container.
- G4. Every pause and restore is recorded in the event log, including *why*
  processes were not kept, so a trace shows exactly what survived.

**Non-goals (this design)**
- A manual `adlc suspend <run>` command for a run that is not waiting on
  approval. (AX has `ax suspend`; adlc's harness runs in the `adlc` process on
  the host, so suspending mid-step is a different problem.)
- Snapshots for crash recovery. A crashed run had no chance to snapshot; the
  existing `adlc resume` behaviour is unchanged.
- Moving a paused run to another machine.
- Eval runs (`internal/evals`) — they never park.
- Forking from a snapshot (AX's "Stateful Task Branching"); `adlc fork` stays
  as it is.

**Anti-goals (would make this a failure even if it works)**
- A host without CRIU loses the ability to pause. Files-only must always work.
- Silent loss: a restore that drops processes without saying so in the log
  and on stdout.
- Weakening sandbox isolation (e.g. turning seccomp off) by default to make
  CRIU work.

## 3. Constraints (found in the code and docs)

- **Docker checkpoint is experimental.** Docker's docs: "Checkpoint and
  Restore is an experimental feature", CRIU is "an external dependency", and
  it is "currently focused on single-host use cases". Their example runs with
  `--security-opt=seccomp:unconfined`, and they warn "`seccomp` is only
  supported by CRIU in very up-to-date kernels". adlc's `hostConfig` keeps the
  default seccomp profile, drops all capabilities, and sets
  `no-new-privileges`. Whether CRIU works with that is **unknown** — see §8.
- **A process checkpoint needs the same container.** Restore is
  `docker start --checkpoint <id> <container>`; if the container is removed,
  the checkpoint is gone with it. Process checkpoints therefore stop the
  container (`CheckpointCreateOptions.Exit = true`) but do not remove it.
- **The SDK supports it.** `github.com/moby/moby/client v0.6.3` (already in
  `go.mod`) has `CheckpointCreate(ctx, id, CheckpointCreateOptions{CheckpointID, CheckpointDir, Exit})`,
  `CheckpointList`, `CheckpointRemove`, and `ContainerStartOptions{CheckpointID, CheckpointDir}`.
  `system.Info.ExperimentalBuild` tells whether the daemon runs in
  experimental mode. No new dependency is needed.
- **The event log refuses events on a paused run.** `eventlog/fold.go`:
  "paused run can only continue with approval_resolved". The new events must
  be allowed there (§5.3).
- **Protobuf rules** (AGENTS.md): only add fields and messages; never renumber.
  `Checkpoint.snapshot_ref` (field 2) already exists and is unused.
- **Workspace size cap.** `Backend.Create` copies `WorkspaceDir` with
  `sandbox.MaxWorkspaceBytes`. A restored workspace larger than that cap
  cannot be recreated through `Create`.
- **Image drift.** Restoring files into a new container is only safe if the
  image is the same. Agent files today use tags (`golang:1.26`). Digest pinning
  is the next separate piece of work (gap 3); until then this design records
  the image ID at suspend time and restores from that ID (§5.2).
- **No Docker in CI or in the cloud sessions used so far.** Unit tests use
  fakes (`internal/sandbox/sandboxtest`); real-Docker tests are integration
  tests, as for `isolation_integration_test.go`.

## 4. Approaches considered

| | Approach | Trade-off |
|---|---|---|
| **A (chosen)** | **Two layers.** Always save `/workspace` as a tar. If the host supports it, also checkpoint processes and stop the container. Restore from the process checkpoint if usable, else from files in a new container, and log that processes were lost. | Full fidelity where possible, never worse than files-only. Two restore paths to test. |
| B | Process checkpoints only. Pausing requires experimental Docker + CRIU. | Simpler, but hosts without CRIU cannot pause at all, and a removed container loses everything. |
| C | Declarative restart. The agent declares background processes; adlc restarts them after restoring files. | Works everywhere, but processes restart instead of continuing. |

The owner picked **A**.

## 5. Design

### 5.1 Sandbox interface

Add an optional interface in `internal/sandbox` so backends without
snapshot support keep compiling and the fake can implement it:

```go
// Suspender is implemented by backends that can pause a sandbox.
type Suspender interface {
    // Suspend saves /workspace to a tar archive and stops the sandbox from
    // using CPU and memory. It tries a process checkpoint first when the
    // backend supports one; the result says what was kept.
    Suspend(ctx context.Context, h Handle, opts SuspendOptions) (Snapshot, error)
    // Restore brings back a sandbox from snap. It returns the handle to use
    // (which may be a new container) and what survived.
    Restore(ctx context.Context, spec Spec, snap Snapshot) (Handle, RestoreResult, error)
}

type SuspendOptions struct {
    WorkspaceTarPath string // where to write the /workspace archive
    TryProcesses     bool   // attempt a process checkpoint
}

type Snapshot struct {
    WorkspaceTarPath string
    WorkspaceSHA256  string
    WorkspaceBytes   int64
    ImageID          string // resolved image ID at suspend time
    ContainerID      string // set when a process checkpoint was taken
    CheckpointID     string // "" = files only
    ProcessesSkipped string // why no process checkpoint, "" if taken
}

type RestoreResult struct {
    Processes     bool   // true = processes continued from the checkpoint
    FallbackCause string // why the process checkpoint was not used, if it existed
}
```

### 5.2 Docker backend

**Suspend** (in order):
1. `CopyOut(/workspace)` → write to `WorkspaceTarPath`, record size and SHA-256.
   On failure: return the error and leave the container running (same rule as
   `finish` today: "If the copy fails the container is kept").
2. Inspect the container's image → `ImageID`.
3. If `TryProcesses` and `Info().ExperimentalBuild`: `CheckpointCreate` with
   `Exit: true` and `CheckpointID = "adlc-<seq>"`. On success, set
   `ContainerID`/`CheckpointID`; the container is stopped, not removed.
   On failure, record the daemon's error text in `ProcessesSkipped`, then
   remove the container.
4. Without `TryProcesses` or experimental mode: set `ProcessesSkipped`
   (`"daemon is not in experimental mode"` / `"disabled"`) and remove the
   container.

**Restore**:
1. Verify the tar's SHA-256; refuse on mismatch.
2. Check that `snap.ImageID` is still present on the host. If it is not
   (e.g. pruned), refuse with `ErrImageGone` and a message naming the ID; a
   moved tag alone is **not** a reason to refuse, because the restore uses the
   recorded ID.
3. If `CheckpointID != ""` and the container still exists:
   `ContainerStart(..., {CheckpointID})`, then probe with `Exec("true")`.
   Success → `RestoreResult{Processes: true}`. Failure → record the cause,
   remove the container, fall through.
4. Files path: create a fresh container from `snap.ImageID` (not from the
   tag in `spec.Image`), with an empty `/workspace`, then copy the saved tar
   **as is** into it with `CopyToContainer` at `/`. Do **not** go through
   `Create(spec with WorkspaceDir)`: `sandbox.WorkspaceTar` skips "Symlinks and
   special files", resets every owner to `SandboxUID:SandboxGID` and applies
   the 512 MiB `MaxWorkspaceBytes` cap, so a round trip through it would
   silently change the workspace. `RestoreResult{Processes: false,
   FallbackCause: ...}`.

**What the files path keeps and loses** (stated in the stdout line and in
`SandboxRestored.fallback_cause`): it keeps everything under `/workspace`
byte for byte, including symlinks and modes. It loses running processes and
anything outside `/workspace` (e.g. `/tmp`, which is `HOME`, and packages the
agent installed elsewhere).

### 5.3 Event log

Add two payload kinds to `events.proto` (next free oneof numbers):

```proto
SandboxSuspended sandbox_suspended = 23;
SandboxRestored  sandbox_restored  = 24;

message SandboxSuspended {
  string snapshot_ref = 1;        // path of the workspace tar, relative to .adlc/
  string workspace_sha256 = 2;
  int64  workspace_bytes = 3;
  string image_id = 4;
  string checkpoint_id = 5;       // "" = files only
  string processes_skipped = 6;   // reason, "" if processes were checkpointed
}

message SandboxRestored {
  uint64 suspended_seq = 1;       // the SandboxSuspended event restored from
  bool   processes = 2;           // true = processes continued
  string fallback_cause = 3;      // why the process checkpoint was not used
  string sandbox_id = 4;          // handle now in use
}
```

`fold.go` change: while `StatusPaused`, also accept `sandbox_suspended` and
`sandbox_restored` without leaving the paused state. `RunState` gains no
field: `RunState.Digest()` JSON-encodes the whole struct, so a new field would
change the digest of every existing run. Instead the CLI scans the log
(`pendingSnapshot` in `internal/cli/snapshot.go`): the latest
`sandbox_suspended` with no later `sandbox_restored` naming its seq is the
snapshot to restore.
Add a fold test for each, plus one that a paused run still refuses every other
kind.

### 5.4 CLI

- `run_sandbox.go` `finish`: for `STOP_REASON_AWAITING_APPROVAL`, call
  `Suspend` (when the backend implements `Suspender`) instead of only
  `keepForResume`; append `SandboxSuspended`; print one line, e.g.
  `sandbox: paused (files + processes)` or
  `sandbox: paused (files only: daemon is not in experimental mode)`.
  If `Suspend` fails, keep today's behaviour (container kept, message printed)
  and append nothing.
- `attachSandbox` (used by `approve` and `resume`): if the log has a
  `SandboxSuspended` with no later `SandboxRestored` (see §5.3), call `Restore`, append
  `SandboxRestored`, and print what survived. Otherwise keep today's
  live-container lookup.
- **Every command that continues a run handles the sandbox afterwards, not
  only `adlc run`/`adlc fork`.** Today only `run.go` and `fork.go` call
  `finish`; `approve.go`'s `continueRun` calls `attachSandbox` and
  `harness.Approve` and never touches the sandbox again. `continueRun` (and
  `resumeAgent`) must keep the `runSandbox` they attached and call `finish`
  on the returned state, so a run that parks a second time is paused again
  and a run that completes is copied out and destroyed as today.
- **Many pauses per run.** A run can park many times. Each pause writes a new
  `SandboxSuspended` and a new tar; the latest one is the one to restore; the matching `SandboxRestored.suspended_seq` closes it. Older tars may
  be deleted once a later restore has succeeded.
- **Approve fails after a restore** (restore succeeded, then
  `harness.Approve` returns an error before `approval_resolved` is written):
  the run is still paused, so `finish` is called with that paused state and
  suspends the sandbox again. If that suspend fails, the container is kept,
  as today.
- Snapshot files live in `.adlc/snapshots/<run_id>/<seq>.tar`, next to the
  existing `.adlc/workspaces/<run_id>` default.

### 5.5 Agent file

```yaml
sandbox:
  pause: auto   # auto (default) | files | keep
```

- `auto`: files always, processes when the host supports it.
- `files`: never attempt a process checkpoint.
- `keep`: today's behaviour (container stays running); an escape hatch.

## 6. Calls made by the design author (not decided by the owner)

- The `sandbox.pause: auto | files | keep` agent field and its default `auto`.
- Snapshot location `.adlc/snapshots/<run_id>/<seq>.tar`.
- Restore from the recorded image ID instead of refusing when a tag moved.
- Files restore copies the raw tar back (keeps symlinks, modes, size) instead
  of reusing `Create(WorkspaceDir)`.
- Pause and restore wired into `approve` and `resume`, not only `run`/`fork`.
- Manual `adlc suspend`, crash snapshots and eval runs are out of scope.

## 7. Left to the builder

- Splitting into tasks and briefs under `docs/impl/tasks/` (suggested:
  P.1 interface + fake + conformance tests; P.2 event schema + fold;
  P.3 Docker files path; P.4 Docker process path; P.5 CLI wiring + agent field).
- Exact error and stdout wording, following existing messages.
- When to delete snapshot files (suggested: after the run finishes, like the
  container).

## 8. Risks and the spike to run first

- **CRIU vs adlc's isolation settings (highest risk).** Before P.4, run a
  throwaway spike on a Linux Docker host with experimental mode and CRIU:
  create a container with adlc's exact `hostConfig`, start a background
  process through `docker exec`, checkpoint with exit, start from the
  checkpoint, and check the process is still counting. Record the result in
  the work plan's decision log. If it fails only with the default seccomp
  profile, do **not** relax seccomp by default (anti-goal); the files path
  still works, and relaxing is a separate decision for the owner.
- **Spike result (2026-11-14).** Host: Ubuntu 24.04, kernel 6.18.44
  (`CONFIG_CHECKPOINT_RESTORE=y`), Docker 29.9.2 with `--experimental`,
  runc 1.5.2, CRIU 4.3 from `ppa:criu/ppa` (`criu check`: "Looks good.").
  Test: busybox container, a counter started with `docker exec -d`, then
  `docker checkpoint create` and `docker start --checkpoint`.
  - Checkpoint **succeeds** with `--network none`, `--init`, `--cap-drop ALL`,
    `no-new-privileges`, and pids/memory limits, each tested on its own.
  - Checkpoint **fails** with `--user 65532:65532` (adlc's sandbox user):
    CRIU logs "Can't get rlimit 0: Operation not permitted".
  - Restore **fails in every case**, including a container with default
    settings: `bind-mount /proc/0/ns/net -> /var/run/docker/netns/...: no such
    file or directory`. Restoring into a new container with
    `--checkpoint-dir` is refused: "custom checkpointdir is not supported".
  - Conclusion: on this Docker version the process path (P.4) cannot work,
    independent of adlc's settings. P.4 is deferred until a Docker version
    where restore works is found; the files path (P.1–P.3, P.5) does not
    depend on it.
- **`docker exec` processes.** It is not verified that processes started via
  `exec` (how adlc's tools run commands) are restored correctly; the spike
  must test exactly that.
- **Disk use.** Each pause writes a full workspace tar; process checkpoints
  add process memory to Docker's storage.
