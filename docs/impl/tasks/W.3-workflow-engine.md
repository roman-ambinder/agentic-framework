# W.3 — Workflow engine: workspace, tasks, checks, budgets, resume

Size: L · Depends on: W.1, W.2 · Branch: `task/W.3-workflow-engine` · PR title: `W.3: workflow engine runs tasks on a shared workspace with checks, budgets and resume`

## Goal

After this task, package `internal/workflow` can **run** a workflow file
(task W.1) and record its history in a workflow log (task W.2):

- `New(Options)` builds an `Engine`; `Start` creates the workflow's workspace
  from its inputs, writes `workflow_started` and runs every task in
  `Workflow.Order()`, one at a time; `Resume` folds the log and continues a
  workflow that crashed, was interrupted or is parked.
- Each agent task: render the prompt (`Render` with real `Sources`), nest the
  budget (m3.md decision 7), write `task_started`, copy the workspace into a
  fresh sandbox, call `Options.RunTask`, copy `/workspace` out, **drop every
  `.git`**, replace the workspace, commit it with host-side git, run the
  task's `checks:` in another fresh sandbox, write `task_finished`. Failed
  attempts are retried up to `attempts` with the check output added to the
  prompt. A child run that parks for a tool approval parks the workflow.
- The workflow ends with exactly one `workflow_completed`.

Review (task W.4), gates and export (task W.5) are **not** implemented here:
this task leaves clearly marked stub functions that those tasks replace
(see "Hooks for W.4 and W.5"), so W.3 compiles and is tested on its own.
There is no CLI in this task (`adlc workflow …` is task W.5); `RunTask` and
`AgentBudget` are wired by the CLI later, tests pass fakes.

## Read first

- `AGENTS.md`; `docs/impl/m3.md` (all of it: decisions 1–8 and 10 are
  implemented here; the API section is the contract)
- `docs/impl/tasks/W.1-workflow-file.md` (`Workflow`, `Task`, `Budget`,
  `Order`, `Sources`, `Render`, `MaxTemplateValueBytes`, `TruncatedMarker`)
- `docs/impl/tasks/W.2-workflow-events.md` (the six payloads, `Fold`,
  `State`, `TaskState`, `eventlog.WithFirstKind`)
- `docs/impl/interfaces.md` §3 (event log), §6 + §6.2 (harness, stop
  reasons), §8 (sandbox, `sandboxtest.Host`), §9 (evals)
- `internal/evals/runner.go`: `runAttempt`/`run` (one sandbox per run,
  `CopyOut` + extract, a *fresh* sandbox for grading), `changedFiles` and
  `gitOutput` — **why** git always gets a trusted host-side `--git-dir`
  (the security fix of task 1.8), and `internal/evals/changed_test.go`
  (`TestChangedFilesIgnoresAgentGitConfig`, the model for the security test here)
- `internal/evals/tarx.go` (`ExtractWorkspaceTar`), `internal/evals/workspace.go` (`gitRun`, `gitCommitBase`: identity `adlc <adlc@localhost>`)
- `internal/sandbox/sandbox.go`, `internal/sandbox/sandboxtest/host.go`
- `internal/eventlog/store.go` (`Append` with a non-zero `Seq`, `ErrSeqConflict`, `Read`, `State`, `ErrRunNotFound`), `internal/eventlog/fold.go` (`RunState`, `Usage`, `StatusPaused`)
- `internal/harness/loop.go` (`usage()`: how the harness counts tokens), `internal/harness/budget.go` (detail texts)
- `internal/cli/eval.go` `evalRunAgent` and `internal/cli/run_sandbox.go` — how the CLI will later wire a child run with sandbox tools. **Do not change the CLI in this task.**
- `internal/cli/approve.go` `continueRun` / `internal/cli/resume.go` `attachSandbox`: `adlc approve` finds a run's sandbox by its `adlc.run_id` label — this is why the child sandbox's `Spec.RunID` is the child run id.

## Files

Create (package `internal/workflow`):

```
internal/workflow/engine.go        Options, TaskRun, Engine, New, Start, Resume, Workspace, the four errors,
                                   flow, drive, next, action, execRun, execContinue, afterRun,
                                   finishProducer, finishTask, add, childRunID, commitMessage, progress lines,
                                   the hook block for W.4/W.5
internal/workflow/history.go       runRecord, taskProgress, progressOf, lastCommit (scans of the workflow log)
internal/workflow/workspace.go     the workflow directory layout, git helper (runGit, gitRepo), workspace
                                   creation from the repo input, input snapshots, collect (copy out, drop .git,
                                   replace, commit), restore
internal/workflow/sources.go       flowSources (the Sources implementation), retryNote, the prompt of a run
internal/workflow/budget.go        spend, spent, nestBudget
internal/workflow/checks.go        runChecks, checkDetail, tail, checkTimeout
internal/workflow/helpers_test.go  test clock, stores, repo builder, fake RunTask, recording sandbox, truncateLog
internal/workflow/engine_test.go
internal/workflow/history_test.go
internal/workflow/workspace_test.go
internal/workflow/security_test.go
internal/workflow/sources_test.go
internal/workflow/budget_test.go
internal/workflow/checks_test.go
internal/workflow/resume_test.go
```

Change (shared files, keep the change to the lines named):

```
internal/evals/runner.go     add GoEnv() (5 lines, below goEnv); nothing else
docs/impl/interfaces.md      append the subsection "W.3 — engine" at the end of §12 (text under "interfaces.md text"); nothing else
```

No new dependencies (`google.golang.org/protobuf` with `durationpb`,
`timestamppb` and `proto` is already in `go.mod`). No change to any
`.proto` file (task W.2 adds the payloads). Do not touch `internal/cli`,
`internal/harness`, `go.mod`, `Makefile`.

## Design

### `evals.GoEnv` (`internal/evals/runner.go`)

```go
// GoEnv returns a copy of the environment the eval sandboxes get. The
// workflow engine uses it as its default sandbox environment.
func GoEnv() map[string]string { return maps.Clone(goEnv) }
```

`internal/workflow` imports `internal/evals` (for `DefaultImage`,
`ExtractWorkspaceTar`, `GoEnv`). Therefore **`internal/evals` must never
import `internal/workflow`** (import cycle); say so in the PR.

### Exported API (`engine.go`) — exactly as m3.md

```go
// TaskRun is one child agent run of a workflow task.
type TaskRun struct {
	RunID      string
	WorkflowID string
	Task       Task
	Role       adlcv1.TaskRole
	AgentRef   string         // Task.Agent, or Review.Critic for critics (unresolved: the CLI resolves it against Workflow.Dir)
	Prompt     string         // rendered
	Budget     *adlcv1.Budget // after nesting (decision 7)
	Sandbox    sandbox.Sandbox
	Handle     sandbox.Handle // /workspace holds the current workspace
}

// Options are the engine's dependencies.
type Options struct {
	Log         *eventlog.Store   // the workflow log (decision 2), opened WithFirstKind("workflow_started")
	Runs        *eventlog.Store   // the run log, read-only here (child states)
	Sandbox     sandbox.Sandbox
	Image       string            // "" = evals.DefaultImage
	Env         map[string]string // sandbox env; nil = evals.GoEnv()
	BaseDir     string            // decision 4: <dir of --workflow-db>/workflows (the CLI computes it); made absolute by New
	RunTask     func(ctx context.Context, tr TaskRun) (*eventlog.RunState, error)
	AgentBudget func(ref string) (*adlcv1.Budget, error) // the agent's own budget, for nesting
	Now         func() time.Time  // nil = time.Now
	Stdout      io.Writer         // progress lines; nil = io.Discard
}

// Engine runs workflows. It keeps no state between calls: everything is in the log.
type Engine struct{ o Options }

func New(o Options) (*Engine, error)
func (e *Engine) Start(ctx context.Context, w *Workflow, id string, inputs map[string]string) (*State, error)
func (e *Engine) Resume(ctx context.Context, w *Workflow, id string) (*State, error)
func (e *Engine) Workspace(id string) string // filepath.Join(BaseDir, id, "workspace")

var (
	ErrWorkflowExists   = errors.New("workflow: workflow already exists")
	ErrWorkflowNotFound = errors.New("workflow: workflow not found")
	ErrWorkflowChanged  = errors.New("workflow: file changed since the workflow started") // digest mismatch on Resume
	ErrWorkflowFinished = errors.New("workflow: workflow already completed")
)
```

`Options.Gates` (`GatePolicy`) and `Options.SkipExport` from m3.md are **not**
added here: their type and meaning belong to W.5/W.6, which add the fields.
Do not add any other exported name.

**`New`** returns these errors, checked in this order (exact text):
`workflow: options: Log is nil`, `workflow: options: Runs is nil`,
`workflow: options: Sandbox is nil`, `workflow: options: RunTask is nil`,
`workflow: options: AgentBudget is nil`, `workflow: options: BaseDir is empty`.
It fills the defaults (Image, Env, Now, Stdout) and replaces `BaseDir` with
`filepath.Abs(BaseDir)` (error `workflow: options: base dir: %w`). Absolute
paths matter: git commands run with `cmd.Dir` set to the workflow
directory, so relative `--git-dir` values would break.

### Layout on disk (`workspace.go`)

```
<BaseDir>/<id>/workspace/       the workspace: a plain directory, never containing .git (decision 4)
<BaseDir>/<id>/git/             the engine's git directory for the workspace
<BaseDir>/<id>/inputs/<name>    snapshot of each kind: file input, copied at Start
<BaseDir>/<id>/incoming/        temporary: what a sandbox's /workspace became (collect)
<BaseDir>/<id>/previous/        temporary: the old workspace during the swap
```

```go
// flow is one workflow being driven by this process.
type flow struct {
	w       *Workflow
	id      string
	dir     string  // <BaseDir>/<id>
	ws      string  // dir/workspace
	git     gitRepo // gitDir dir/git, workTree ws
	lastSeq uint64  // seq of the last event this process read or wrote
}
```

`removeUnder(base, p string) error` is the only way the engine deletes a
directory: it refuses (`workflow: refusing to remove %s outside %s`) unless
`filepath.Rel(base, p)` is local (`filepath.IsLocal`) and not `"."`, then
calls `os.RemoveAll(p)`. Always pass `base = f.dir`.

### Git helper (`workspace.go`)

All host-side git goes through one function:

```go
// runGit runs git with args and returns stdout. It never reads the user's
// or the system's git config, never runs hooks or fsmonitor, and commits as
// "adlc <adlc@localhost>" at time now.
func runGit(ctx context.Context, dir string, now time.Time, args ...string) ([]byte, error)
```

- argv: `git` + these flags, then `args`:
  `-c core.hooksPath=/dev/null -c core.fsmonitor=false -c commit.gpgsign=false
  -c core.autocrlf=false -c gc.auto=0 -c user.name=adlc -c user.email=adlc@localhost`
- `cmd.Dir = dir` (always an engine-owned directory: the flow dir).
- `cmd.Env`: start from `os.Environ()`, **drop every variable whose name starts
  with `GIT_`** (a `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` or
  `GIT_CONFIG_*` in the user's shell must not redirect us), then add
  `GIT_CONFIG_NOSYSTEM=1`, `GIT_CONFIG_GLOBAL=/dev/null`, `GIT_TERMINAL_PROMPT=0`,
  `GIT_CEILING_DIRECTORIES=<filepath.Dir(dir)>`,
  `GIT_AUTHOR_DATE=<unix> +0000` and `GIT_COMMITTER_DATE=<unix> +0000`
  (`fmt.Sprintf("%d +0000", now.Unix())`, git's internal date format).
- Error: `fmt.Errorf("workflow: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr))`.

**Identity decision: `adlc <adlc@localhost>`**, the identity
`internal/evals/workspace.go` already uses for its base commits. These
commits live only in the engine's private git directory; they record what
the *engine* did, so they carry the tool's name, never the user's (we do not
read the user's config at all) and never the name of a model or an agent product. The
fixed dates from `Options.Now` make commit hashes deterministic in tests.
Exporting to the user's repository (W.5) decides its own identity there.

```go
// gitRepo is the engine's git directory plus the workspace as work tree.
type gitRepo struct {
	dir, gitDir, workTree string // dir = flow dir (cmd.Dir)
	now                   func() time.Time
}

func (g gitRepo) run(ctx context.Context, args ...string) ([]byte, error)
	// runGit(ctx, g.dir, g.now(), "--git-dir="+g.gitDir, "--work-tree="+g.workTree, args...)
func (g gitRepo) init(ctx context.Context) error
func (g gitRepo) commit(ctx context.Context, msg ...string) (string, error)
func (g gitRepo) restore(ctx context.Context, commit string) error
func (g gitRepo) diff(ctx context.Context, from, to string) (string, error)
```

| Method | Commands (each through `g.run`, i.e. with `--git-dir` and `--work-tree`) |
|--------|--------------------------------------------------------------------------|
| `init` | `runGit(ctx, g.dir, now, "init", "-q", "--bare", "--template=", "-b", "main", g.gitDir)` (no `--work-tree`: there is none yet; `--template=` = no hook templates), then `g.run("config", "core.bare", "false")` |
| `commit` | `add -A`; `commit -q --allow-empty --no-verify -m <msg[0]> [-m <msg[1]> …]`; `clean -ffdxq`; `rev-parse --verify HEAD` → trimmed hash |
| `restore` | `removeUnder(dir, workTree)`, `os.MkdirAll(workTree, 0o755)`, `reset -q --hard <commit>` |
| `diff` | `diff --no-color --no-ext-diff --no-textconv --no-renames <from> <to>` → stdout as string |

`--allow-empty`: every run gets its own commit, even when it changed nothing,
so every `task_finished` of a run that was collected has a commit.
`clean -ffdxq` after the commit removes files the workspace's own
`.gitignore` ignores, so **the workspace always equals its last commit** (the
checks run "on the committed workspace", decision 8, and `restore` can
rebuild it exactly). Commit messages:

```go
// commitMessage is "task <id> attempt <n>", plus " round <r>" when round > 0 (task W.4).
func commitMessage(taskID string, attempt, round uint32) string
```

### Workspace from the inputs (`workspace.go`)

`Start` validates inputs before touching the disk:

1. Every `w.Inputs[i].Name` must be in `inputs` (`workflow: missing input %q`);
   every key of `inputs` must be declared (`workflow: unknown input %q`);
   a name must satisfy `filepath.IsLocal(name) && !strings.ContainsAny(name, "/\\")`
   (`workflow: invalid input name %q`).
2. Per kind:
   - `text`: any value.
   - `file`: `os.Stat` must be a regular file (`workflow: input %q: %s is not a regular file`).
   - `dir`: must be a directory (`workflow: input %q: %s is not a directory`).
     The input named **`repo`** (unexported `const repoInput = "repo"`; W.5
     defines the exported `ExportInput` with the same value) must also have a
     `.git` **directory** (`<repo>/.git`, `os.Stat(...).IsDir()`), else
     `workflow: input "repo": %s is not a git repository with a .git directory`.
     An input named `repo` of another kind: `workflow: input "repo" must be kind dir`.

The workspace is made from the `repo` input; a workflow without one starts
with an empty workspace.

**Decision: only committed files of `repo`, at its `HEAD`.** The engine runs
(with `runGit`, `dir` = flow dir):

```
git --git-dir=<abs repo>/.git rev-parse --verify -q "HEAD^{commit}"        → head (error: workflow: input "repo": %s has no commit)
git --git-dir=<abs repo>/.git archive --format=tar --prefix=workspace/ "<head>^{tree}"   → tar bytes
```

and unpacks the tar with `evals.ExtractWorkspaceTar(tar, f.ws)`. Why: the
result is reproducible (it is exactly commit `head`), it never contains the
repo's `.git` or its untracked/ignored files (build outputs, secrets in
`.env` files the user did not commit), and `git archive` needs no work tree,
so nothing runs against the user's working copy. Archiving the **tree**
(`^{tree}`) and not the commit matters: for a commit, git adds a pax global
header (`pax_global_header`) that `ExtractWorkspaceTar` would reject as an
unexpected entry. Symlinks in the repo are skipped by the extractor (the
sandbox does not support them either). Uncommitted changes in `repo` are
ignored; print `workflow <id>: workspace from <repo> at <head> (committed files only)`.

**Input snapshots.** For each `file` input, copy the file to
`<dir>/inputs/<name>` at Start. `Sources.Input` reads the snapshot, so a
resumed workflow renders the same prompts even if the user edited the file.

**Base commit.** After the workspace is filled (or left empty), `g.commit`
with the single message `"base"`. Its hash is `WorkflowStarted.base_commit`;
the repo's `head` is `WorkflowStarted.source_commit` (`""` without a repo).
The field is the only record of the source commit: export (W.5) bases its
branch on it (m3.md decision 11).

### Collecting a sandbox's workspace (`workspace.go`)

```go
// collect copies /workspace out of h, drops every .git, makes it the
// workflow's workspace and commits it. It returns the commit hash and the
// .git paths it dropped (relative, slash-separated, sorted).
func (e *Engine) collect(ctx context.Context, f *flow, h sandbox.Handle, msg string) (string, []string, error)
```

1. `data, err := e.o.Sandbox.CopyOut(ctx, h, sandbox.WorkspacePath)`.
2. `in := filepath.Join(f.dir, "incoming")`; `removeUnder(f.dir, in)`;
   `evals.ExtractWorkspaceTar(data, in)`.
3. `dropped, err := dropGit(in)`: walk `in`; every entry (directory **or file**)
   whose name satisfies `strings.EqualFold(name, ".git")` is removed with
   `os.RemoveAll` and not descended into. Case-insensitive because on a
   case-insensitive file system `.GIT` is the same directory.
4. Swap: `prev := filepath.Join(f.dir, "previous")`; `removeUnder(f.dir, prev)`;
   `os.Rename(f.ws, prev)`; `os.Rename(in, f.ws)`; `removeUnder(f.dir, prev)`.
5. `hash, err := f.git.commit(ctx, msg)`.
6. If `dropped` is not empty, print `task <task>: dropped .git: <a, b>`.

**Why the drop.** An agent controls everything in `/workspace`, including a
`.git` directory with a `config` that sets `core.fsmonitor`, `core.hooksPath`
or filter drivers — programs host-side git would run. The engine's git
already reads only its own `--git-dir`, but a `.git` left in the workspace
would be copied into the next sandbox, committed as content by `git add`
(nested `.git` directories become embedded repositories) and handed to the
user by export. Dropping every `.git` at any depth, before git sees the
tree, removes all of that (m3.md decision 4, lesson of task 1.8).

### History scans (`history.go`)

`Fold` (W.2) gives the per-task summary; the engine also needs the exact
records (details, commits, times), so it scans the same events:

```go
// runRecord is one task_started and its task_finished (nil while open).
type runRecord struct {
	Started    *adlcv1.TaskStarted
	Finished   *adlcv1.TaskFinished
	StartedAt  time.Time // event time of task_started
	FinishedAt time.Time // event time of task_finished; zero while open
}

// taskProgress is the history of one task.
type taskProgress struct {
	TaskID     string
	Records    []runRecord // every task_started of the task, in log order (all roles)
	BaseCommit string      // last commit recorded before the task's first task_started; "" if not started
}

func progressOf(events []*adlcv1.Event, taskID string) taskProgress
func (p taskProgress) last() *runRecord                             // nil if no records
func (p taskProgress) lastOf(role adlcv1.TaskRole) *runRecord       // nil if none
func (p taskProgress) failedInRound(round uint32) int               // PRODUCER records of that round with outcome CHECKS_FAILED or RUN_FAILED
func (p taskProgress) lastFailure(round uint32) *runRecord          // last PRODUCER record of that round with CHECKS_FAILED or RUN_FAILED
func (p taskProgress) lastCommit() string                           // last non-empty TaskFinished.commit of the task; "" if none

// lastCommit is the last commit recorded anywhere in the log:
// workflow_started.base_commit or a later non-empty task_finished.commit.
func lastCommit(events []*adlcv1.Event) string
```

A `task_finished` belongs to the newest open record of the same `task_id`.
"Commit recorded before" means: walk the events in order, remember the
latest non-empty commit (`base_commit`, then every `task_finished.commit`),
and take its value at the task's first `task_started`.

### Child run ids (m3.md decision 14)

```go
// childRunID is "<workflowID>-<n>".
func childRunID(workflowID string, n int) string

// nextRunNumber is 1 + the number of distinct non-empty TaskStarted.run_id values in events.
func nextRunNumber(events []*adlcv1.Event) int
```

A new child run gets `childRunID(f.id, nextRunNumber(events))`, with `events`
read just before its `task_started` is written: `t-v1-20261114T101500Z-abcdef-1`,
`…-2`, … across all tasks and roles. A **continuation** of a parked run
writes a new `task_started` with the **same** run id (decision 10), so it
does not count again. An `ERROR` restart is a new run and gets the next
number. Export tasks have `run_id ""` and do not count. The workflow id has
at most 58 characters, so every child run id fits the repository's run-id
rule `^[A-Za-z0-9._-]{1,64}$` up to 99 999 runs; `adlc kill run`, `trace` and
`cost` work on it unchanged. The number is derived from the log only, so a
resumed workflow continues the sequence.

### Budgets (`budget.go`)

```go
// spend is what a workflow has used so far.
type spend struct {
	USDMicros int64
	Tokens    int64
	WallClock time.Duration
}

// spent sums the usage of every child run named in the log and the time
// tasks were active.
func (e *Engine) spent(ctx context.Context, events []*adlcv1.Event) (spend, error)

// nestBudget returns the run budget of the next child run, or a non-empty
// detail when the workflow budget is already spent.
func nestBudget(agent *adlcv1.Budget, task, wf Budget, used spend) (*adlcv1.Budget, string)
```

**`spent`, exact algorithm.**

1. Run ids: every non-empty `TaskStarted.run_id` in the log, each once.
2. For each: `e.o.Runs.State(ctx, id)`. `errors.Is(err, eventlog.ErrRunNotFound)`
   → skip (`RunTask` failed before the run wrote anything). Other error → return it.
   `Tokens += InputTokens + OutputTokens + CacheReadTokens + CacheWriteTokens`
   (the same sum the harness budgets, `loop.usage()`), `USDMicros += Usage.USDMicros`.
3. Wall clock = the time tasks were **active**, not the time since
   `workflow_started`. For every run record of every task:
   - finished, not interrupted: `FinishedAt − StartedAt`;
   - finished with `ERROR` and `detail == interruptedDetail` (`"interrupted"`): `child.LastEventAt − StartedAt`
     if the child run exists and `LastEventAt` is after `StartedAt`, else 0;
   - open (no `task_finished`): `now − StartedAt`.
   Negative values count as 0.

**Decision: active time, not time since start.** A workflow parked at an
approval gate overnight must not run out of a `max_wall_clock: 1h` budget
before the human answers, and the hours a crashed engine was down must not
count either. This is the harness's own rule (time spent paused does not
count against `max_wall_clock`, interfaces.md §6) lifted to workflows. A
parked child's interval ends at its `task_finished{AWAITING_APPROVAL}`; its
continuation opens a new interval.

**`nestBudget`, exact algorithm.**

1. Workflow remainder, checked in this order (the harness's order); the first
   that is spent returns `(nil, detail)`:
   - `wf.MaxWallClock > 0 && used.WallClock >= wf.MaxWallClock` →
     `fmt.Sprintf("max_wall_clock %s reached (used %s)", wf.MaxWallClock, used.WallClock)`
   - `wf.MaxUSDMicros > 0 && used.USDMicros >= wf.MaxUSDMicros` →
     `fmt.Sprintf("max_usd_micros %d reached (used %d)", wf.MaxUSDMicros, used.USDMicros)`
   - `wf.MaxTokens > 0 && used.Tokens >= wf.MaxTokens` →
     `fmt.Sprintf("max_tokens %d reached (used %d)", wf.MaxTokens, used.Tokens)`
2. `out := proto.CloneOf(agent)` (`&adlcv1.Budget{}` when `agent` is nil). Steps
   and tool calls stay as the agent set them (workflows and tasks have no
   such limits).
3. Field-wise minimum of the **positive** values (0 means "no limit" everywhere):
   - `out.MaxUsdMicros = minPositive(agent.MaxUsdMicros, task.MaxUSDMicros, wf.MaxUSDMicros − used.USDMicros if wf.MaxUSDMicros > 0)`
   - `out.MaxTokens = minPositive(agent.MaxTokens, task.MaxTokens, wf.MaxTokens − used.Tokens if wf.MaxTokens > 0)`
   - `out.MaxWallClock = durationpb.New(minPositive(...wall clock, same pattern...))`, or nil when the minimum is 0
4. Return `out, ""`.

```go
// minPositive returns the smallest value > 0, or 0 if there is none.
func minPositive[T int64 | time.Duration](vs ...T) T
```

Mapping: `workflow.Budget.MaxUSDMicros` → `adlcv1.Budget.max_usd_micros`,
`MaxTokens` → `max_tokens`, `MaxWallClock` → `max_wall_clock`.
**Decision: the task's `budget:` limits each run of the task** (every
attempt and, with W.4, every round and the critic), exactly as decision 7
reads: "its run budget is the agent's own budget, narrowed by the task's".
The total across runs is limited by the workflow budget. The workflow
budget itself is `w.Budget`; `WorkflowStarted.budget` records it with only
`max_usd_micros`, `max_tokens`, `max_wall_clock` set.

When `nestBudget` returns a detail, the engine writes
`workflow_completed{BUDGET_EXHAUSTED, "workflow budget: " + detail}` and no
`task_started` (decision 7: a task never starts when the budget is spent).
A child run that hits the remaining budget mid-run simply ends
`BUDGET_EXHAUSTED` (outcome `RUN_FAILED`); the next run's check then stops
the workflow.

### Checks (`checks.go`)

```go
// checkTimeout bounds each check command (same as the sandbox shell tool's maximum).
const checkTimeout = 10 * time.Minute

// maxCheckTailBytes is how much of a failing check's output is kept.
const maxCheckTailBytes = 4 << 10

// runChecks runs t.Checks in order in a fresh sandbox made from the
// committed workspace and stops at the first failure. ok is true when
// every check exited 0. err is an infrastructure failure.
func (e *Engine) runChecks(ctx context.Context, f *flow, t Task, runID string) (detail string, ok bool, err error)

// checkDetail describes failing check i (0-based) of n.
func checkDetail(i, n int, check string, res sandbox.ExecResult) string

// tail returns the last max bytes of b, starting at a UTF-8 rune boundary, and whether it cut.
func tail(b []byte, max int) ([]byte, bool)
```

- No checks → `"", true, nil` without creating a sandbox.
- Sandbox spec: like the run's (below) with `RunID: runID + ".check"` and
  `WorkspaceDir: f.ws` (the workspace equals the commit). `defer` destroy
  with `context.WithoutCancel(ctx)`.
- Each check runs as `[]string{"sh", "-c", "exec 2>&1\n" + check}` with
  `checkTimeout`: `exec 2>&1` merges stderr into stdout inside the shell, so
  the output keeps its real order. Output = `Stdout` followed by `Stderr`
  (normally empty). `Exec` error → return it.
- A check fails when `ExitCode != 0 || TimedOut`.

`checkDetail` format (exact; `%s` of `checkTimeout` is `10m0s`):

```
check <i+1> of <n> failed: <check>
exit code <code>[, timed out after <checkTimeout>]
output:                         ← or "output (last 4096 bytes):" when tail cut
<tail, trailing "\n" trimmed; "(no output)" when empty>
```

`tail`: if `len(b) <= max` return `b, false`; else `b = b[len(b)-max:]`, drop
leading bytes while `!utf8.RuneStart(b[0])`, return `b, true`.

### Sandboxes of a run

```go
spec := sandbox.Spec{
	RunID: runID, Image: e.o.Image, WorkspaceDir: f.ws,
	CPUs: 2, MemoryBytes: 4 << 30, Network: false, Env: e.o.Env,
}
```

(the eval runner's values). `Spec.RunID` is the **child run id** so that
`adlc approve` can find a parked run's sandbox by its `adlc.run_id` label.
Every `Destroy` uses `context.WithoutCancel(ctx)`; a failed destroy prints
`sandbox <id>: destroy failed: <err>` and is otherwise ignored.

### Prompts (`sources.go`)

```go
// flowSources implements Sources for one render.
type flowSources struct {
	ctx    context.Context
	e      *Engine
	f      *flow
	st     *State
	events []*adlcv1.Event
}
```

| Method | Returns | Errors (exact text after `workflow: `) |
|--------|---------|----------------------------------------|
| `Input(name)` | `text`: the value from `st.Inputs`; `file`: contents of `<dir>/inputs/<name>` | not declared: `unknown input %q`; kind `dir`: `input %q is a directory; its files are in the workspace, use file "PATH"` |
| `File(path)` | contents of `filepath.Join(f.ws, path)` | `!filepath.IsLocal(path)`: `file %q is outside the workspace`; missing or not a regular file (`os.Lstat`): `file %q not found in the workspace` |
| `Result(task)` | `Runs.State(<run id of the task's last PRODUCER record with outcome PASSED>).Result` | task unknown or `!st.Tasks[task].Passed`: `result %q: task has not passed` |
| `Diff(task)` | `f.git.diff(p.BaseCommit, p.lastCommit())` | not passed: `diff %q: task has not passed` |

`Render` (W.1) caps every value at `MaxTemplateValueBytes`; the sources
return full values.

```go
// retryNoteFormat is appended to the prompt of a retry; %d is the failed
// attempt, %s its task_finished detail.
const retryNoteFormat = "\n\n## Previous attempt\n\nAttempt %d of this task did not pass. The workspace contains the changes from that attempt.\n\n%s\n\nFix the problem. The task passes only when your run completes and every check passes.\n"
```

**Prompt of a producer run** = `Render(t, sources)` + `e.reviewNote(...)`
(W.4 hook, `""` in W.3) + the retry note of `p.lastFailure(round)` if there
is one (`fmt.Sprintf(retryNoteFormat, rec.Started.Attempt, rec.Finished.Detail)`).
Everything comes from the log and the workspace, so a resumed workflow
builds the same prompt. A `Render` error is not retried: the engine writes
`workflow_completed{FAILED, "task <id>: render prompt: <err>"}` without a
`task_started`.

**Decision: a retry continues from the failed attempt's commit.** Every run
that returned a state (completed or not) is collected and committed, and the
next attempt starts from that workspace. The retry note says so; the agent
fixes what the checks reported instead of starting over, and the same rule
serves W.4's review rounds (the critic's comments refer to that workspace).

### Driving a workflow (`engine.go`)

`Start` and `Resume` prepare, then call one loop that reads the log, decides
the next step from it and does it — so a crash at any event boundary
continues correctly:

```go
type actionKind int

const (
	actRun      actionKind = iota + 1 // start a child run (spec)
	actContinue                       // a parked child run finished: collect it (rec)
	actPark                           // a child run waits for approval: stop driving (rec)
	actComplete                       // write workflow_completed (outcome, detail)
	actExport                         // kind: export (task W.5)
	actReload                         // a hook wrote events: read the log again
)

type runSpec struct {
	Role                  adlcv1.TaskRole
	AgentRef              string
	Attempt, Round        uint32
	Prompt                string
}

type action struct {
	kind    actionKind
	task    Task
	spec    runSpec
	rec     *runRecord
	outcome adlcv1.WorkflowOutcome
	detail  string
}

func (e *Engine) drive(ctx context.Context, f *flow) (*State, error)
func (e *Engine) next(ctx context.Context, f *flow, st *State, events []*adlcv1.Event) (action, error)
```

**Appending.** Every event the engine writes goes through

```go
// appendEvent appends ev to workflow id's log with RunId id, Time e.o.Now()
// and Seq after+1, always with context.WithoutCancel(ctx), and returns the stored event.
func (e *Engine) appendEvent(ctx context.Context, id string, after uint64, ev *adlcv1.Event) (*adlcv1.Event, error)

// add is appendEvent(ctx, f.id, f.lastSeq, ev); it advances f.lastSeq.
func (e *Engine) add(ctx context.Context, f *flow, ev *adlcv1.Event) error

// fold reads workflow id's log and folds it (no events: ErrWorkflowNotFound).
func (e *Engine) fold(ctx context.Context, id string) (*State, []*adlcv1.Event, error)

// newFlow returns the flow of workflow w with id (paths from BaseDir), lastSeq 0.
func (e *Engine) newFlow(w *Workflow, id string) *flow
```

W.5's code (gates, `ResolveGate`, export) appends with
`e.appendEvent(ctx, st.WorkflowID, st.LastSeq, ev)` and continues a workflow
with `e.drive(ctx, e.newFlow(w, id))`.

The explicit `Seq` makes `Append` fail with `eventlog.ErrSeqConflict` if
another process wrote to the same workflow; wrap it as
`workflow: %s: another process is writing this workflow: %w`. The engine sets
`Time` itself so the wall-clock budget uses one clock (`Options.Now`).

**`drive` loop.**

1. `events := e.o.Log.Read(ctx, f.id, 0)`; `st, err := Fold(events)`; `f.lastSeq = st.LastSeq`.
2. `st.Status == "completed"` → return `st, nil`.
3. `ctx.Err() != nil` → `add(workflow_completed{CANCELLED, "cancelled"})`, go to 1.
4. `a, err := e.next(ctx, f, st, events)`; error → return `nil, err`.
5. Do `a`:
   - `actComplete`: `add(workflow_completed{a.outcome, a.detail})`; print; go to 1.
   - `actPark`: print `workflow <id>: waiting for approval of run <run-id>`; return `st, nil`.
   - `actRun`: `e.execRun(ctx, f, a.task, a.spec)`; error → return `nil, err`; go to 1.
   - `actContinue`: `e.execContinue(ctx, f, a.task, a.rec)`; same.
   - `actExport`: `e.runExport(ctx, f.w, st, a.task)` (W.5 hook); same.
   - `actReload`: go to 1.

**`next`, exact rules.** For each `t` in `f.w.Order()`, skip it if
`st.Tasks[t.ID] != nil && st.Tasks[t.ID].Passed`; the first task not skipped
is decided below. If every task is skipped: `actComplete{COMPLETED, ""}`.

For task `t`, with `p := progressOf(events, t.ID)`:

1. **Not started** (`p.last() == nil`):
   a. `park, err := e.checkGate(ctx, f.w, st, t)` (W.5 hook, `false, nil` here).
      `park` → return `actPark` with `rec == nil`, which drive treats like a park
      (W.5 prints its own line; for `rec == nil` drive prints nothing and
      returns the state folded again, because the hook may have written events).
      Not `park`, but the log now has events after `st.LastSeq` (the hook wrote,
      e.g. an auto-approved gate; check with `Log.Read(ctx, f.id, st.LastSeq)`)
      → `actReload`, so `f.lastSeq` is fresh before anything else is appended.
   b. `t.Kind == "export"` → `actExport`.
   c. Producer run: attempt 1, round `e.producerRound(t, p)` (W.4 hook, 0 here).
2. `last := p.last()`; `last.Finished == nil` → error
   `workflow: task %s run %s has no task_finished` (Resume closes open records first, so drive never sees one).
3. `t.Kind == "export"` (it has a record but the fold did not mark it passed):
   `actComplete{FAILED, fmt.Sprintf("task %s failed after %d attempt(s): %s", t.ID, 1, firstLine(last.Finished.Detail))}`.
   Export never retries and an export `ERROR` is not restarted (W.5).
   `last.Started.Role != PRODUCER` → `e.nextReviewAction(ctx, f, st, events, t, p)` (W.4 hook).
4. By `last.Finished.Outcome`:
   - `ERROR`: producer run with the same attempt and round (a new run, so a new run id: decision 8).
   - `AWAITING_APPROVAL`: `e.parkedAction(ctx, t, last)`.
   - `CHECKS_FAILED`, `RUN_FAILED`: `n := p.failedInRound(round)`; if `n >= t.Attempts`:
     `actComplete{FAILED, fmt.Sprintf("task %s failed after %d attempt(s): %s", t.ID, n, firstLine(last.Finished.Detail))}`;
     else producer run with `attempt = last.Started.Attempt + 1`, same round.
   - `PASSED` (the fold did not mark the task passed, so it has a review): `e.nextReviewAction(...)`.
   - anything else: error `workflow: task %s: unexpected outcome %s`.

"Producer run" means

```go
// producerRun builds the prompt of a producer run and returns actRun, or actComplete{FAILED} on a render error.
func (e *Engine) producerRun(ctx context.Context, f *flow, st *State, events []*adlcv1.Event, t Task, p taskProgress, attempt, round uint32) (action, error)
```

It builds the prompt (see Prompts) and returns `actRun` with
`runSpec{Role: PRODUCER, AgentRef: t.Agent, …}`, or `actComplete{FAILED, …}`
on a render error.

```go
// parkedAction decides what to do with a run that parked (rec's outcome is AWAITING_APPROVAL).
func (e *Engine) parkedAction(ctx context.Context, t Task, rec *runRecord) (action, error)
```

`child, err := e.o.Runs.State(ctx, rec.Started.RunId)`: error → return it;
`child.Status == eventlog.StatusPaused` → `actPark`; `StatusFinished` →
`actContinue`; `StatusRunning` → error
`workflow: run %s is still running; let it finish (adlc resume %s) before resuming the workflow`.

**`execRun(ctx, f, t, spec)`**, in this order:

1. `used, err := e.spent(ctx, events)` (re-read the events); `ab, err := e.o.AgentBudget(spec.AgentRef)`
   (error → `workflow: task %s: agent budget: %w`, nothing written).
2. `b, detail := nestBudget(ab, t.Budget, f.w.Budget, used)`; detail ≠ "" →
   `add(workflow_completed{BUDGET_EXHAUSTED, "workflow budget: " + detail})`, return nil.
3. `runID := childRunID(f.id, nextRunNumber(events))`.
4. `h, err := Sandbox.Create(ctx, spec of the run)`; error → return
   `workflow: task %s: create sandbox: %w` with **nothing written** (no run
   started; the workflow stays `running` and Resume tries again).
5. `add(task_started{task_id, role, attempt, round, run_id, budget: b, sandbox_id: h.ID})`;
   if the append fails, destroy `h` and return the error. Print
   `task <t>: <role> attempt <a>[ round <r>] started, run <run-id>`.
6. `state, err := e.o.RunTask(ctx, TaskRun{runID, f.id, t, spec.Role, spec.AgentRef, spec.Prompt, b, e.o.Sandbox, h})`.
7. `return e.afterRun(ctx, f, t, rec, h, state, err)` where `rec` is the record just started.

The sandbox is created before `task_started` so that the event can name it
(`TaskStarted.sandbox_id`).

**`execContinue(ctx, f, t, rec)`** (a parked run that has since finished,
m3.md decision 10 and the fold's continuation rule):

1. `id := rec.Finished.SandboxId` (the kept sandbox); `""` → error
   `workflow: task %s: parked run %s has no sandbox id`, nothing written.
2. `add(task_started)` with a copy of `rec.Started`: same task, role,
   attempt, round, **run id**, budget, and `sandbox_id: id`.
3. `state, err := Runs.State(ctx, run id)`.
4. `e.afterRun(ctx, f, t, newRec, sandbox.Handle{ID: id}, state, err)` — the
   producer path copies the kept sandbox out (`collect`), commits, runs the
   checks and destroys the sandbox. If the sandbox is gone, `collect` fails
   → `ERROR` (row 5), and the next Resume starts the same attempt as a new run.

```go
// afterRun finishes a child run by role. W.3 knows PRODUCER; CRITIC is W.4's finishCritic.
func (e *Engine) afterRun(ctx context.Context, f *flow, t Task, rec *runRecord, h sandbox.Handle, state *eventlog.RunState, runErr error) error {
	switch rec.Started.GetRole() {
	case adlcv1.TaskRole_TASK_ROLE_PRODUCER:
		return e.finishProducer(ctx, f, t, rec, h, state, runErr)
	case adlcv1.TaskRole_TASK_ROLE_CRITIC:
		return e.finishCritic(ctx, f, t, rec, h, state, runErr)
	default:
		return fmt.Errorf("workflow: task %s: role %s is not supported", t.ID, rec.Started.GetRole())
	}
}
```

**`finishProducer`**, exact order (`keep := false; defer: if !keep, destroy h`):

| # | Condition | `task_finished` outcome, detail, commit | Returns |
|---|-----------|------------------------------------------|---------|
| 1 | `ctx.Err() != nil` | `ERROR`, `"cancelled"`, `""` | nil (drive then writes `CANCELLED`) |
| 2 | `runErr != nil` | `ERROR`, `"run task: " + runErr`, `""` | `workflow: task %s: run task: %w` |
| 3 | `state == nil` | `ERROR`, `"run task returned no state"`, `""` | `workflow: task %s: run task returned no state` |
| 4 | `state.StopReason == AWAITING_APPROVAL` | `AWAITING_APPROVAL`, `""`, `""`, **`sandbox_id: h.ID`**; **`keep = true`** | nil |
| 5 | `hash, dropped, err := e.collect(ctx, f, h, commitMessage(...))` fails | `ERROR`, `"collect workspace: " + err`, `""` | `workflow: task %s: collect workspace: %w` |
| 6 | `state.StopReason != COMPLETED` | `RUN_FAILED`, `runStoppedDetail(state)`, `hash` | nil |
| 7 | `detail, ok, err := e.runChecks(...)`: `err != nil` | as row 1 if `ctx.Err() != nil`; else `ERROR`, `"run checks: " + err`, `hash` | nil for cancel, else `workflow: task %s: run checks: %w` |
| 8 | `!ok` | `CHECKS_FAILED`, `detail`, `hash` | nil |
| 9 | otherwise | `PASSED`, `""`, `hash` | nil |

`runStoppedDetail(state)` = `"run stopped: " + strings.TrimPrefix(state.StopReason.String(), "STOP_REASON_")`
plus `": " + state.StopDetail` when it is not empty.

```go
// finishTask writes task_finished for rec (same task_id, role, attempt, round, run_id) and prints the outcome line.
// It sets sandbox_id only for AWAITING_APPROVAL (the kept sandbox): finishTaskKeep.
func (e *Engine) finishTask(ctx context.Context, f *flow, rec *runRecord, outcome adlcv1.TaskOutcome, detail, commit string) error

// finishTaskKeep writes task_finished{AWAITING_APPROVAL, sandbox_id: sandboxID}.
func (e *Engine) finishTaskKeep(ctx context.Context, f *flow, rec *runRecord, sandboxID string) error
```

**Decision: `ERROR` means "this run did not really happen; do it again".**
An `ERROR` (crash, Docker down, copy-out failed, `RunTask` returned an error)
does **not** use up an attempt and does not end the workflow: the engine
returns the error (the CLI exits 1, the workflow stays `running`), and the
next `Resume` repeats the same attempt as a new run, with the next run id
(m3.md decision 8).
Infrastructure problems are fixed by a person; counting them as attempts
would fail workflows with the default `attempts: 1` for reasons that have
nothing to do with the agent. Cancellation is the exception: like the
harness, a cancelled workflow is finished (`CANCELLED`).

**Decision: a parked child parks the workflow; its sandbox is kept.** The
child's sandbox is not destroyed (row 4) so `adlc approve` (extended by W.5)
can continue the run in it. The handle id is in `TaskStarted.sandbox_id` and
`TaskFinished.sandbox_id`; a later `Resume`, in another process, copies that
sandbox's workspace out (`execContinue`). The sandbox's `Spec.RunID` label
is the child run id as well. Fold (W.2) reports the workflow as
`awaiting_approval` after `task_finished{AWAITING_APPROVAL}` and `running`
again after the continuation's `task_started`.

### Start

1. `w == nil` → error `workflow: Start: workflow is nil`. `id` must match
   `^[A-Za-z0-9][A-Za-z0-9_-]{0,57}$` (`workflow: invalid workflow id %q`;
   m3.md decision 14 — `Workflow.ID` always matches).
2. Validate the inputs (see "Workspace from the inputs"). Nothing is written yet.
3. `events, err := Log.Read(ctx, id, 0)`; `len(events) > 0` → `fmt.Errorf("%w: %s", ErrWorkflowExists, id)`.
   `<BaseDir>/<id>` exists → `fmt.Errorf("%w: %s exists (remove it to start again)", ErrWorkflowExists, dir)`.
   The engine never deletes a directory it did not just create.
4. Create `dir`, the input snapshots, `git.init`, the workspace from `repo`,
   the base commit. On any error in this step remove `dir` (`removeUnder(BaseDir, dir)`;
   it did not exist before step 4) and return the error.
5. `add(workflow_started{workflow_name: w.Name, workflow_version: w.Version, workflow_digest: w.Digest,
   inputs, budget, base_commit, source_commit})` with `Seq: 1`. Print `workflow <id>: started, workspace <ws>`.
6. `return e.drive(ctx, f)`.

### Resume

1. `events := Log.Read(ctx, id, 0)`; none → `fmt.Errorf("%w: %s", ErrWorkflowNotFound, id)`.
2. `st, err := Fold(events)` (error returned as is: it wraps `ErrInvalidLog`).
3. `st.Status == "completed"` → return `st, fmt.Errorf("%w: %s (%s)", ErrWorkflowFinished, id, st.Outcome)`.
4. `w.Digest != st.Digest` → `fmt.Errorf("%w: %s: log has %s, file has %s", ErrWorkflowChanged, id, st.Digest, w.Digest)`.
5. `<dir>/git` must be a directory (`workflow: %s: git directory %s is missing`).
6. **Close an interrupted run.** If any task's last record is open
   (`st.Tasks[...].Running`; at most one), `finishTask(ERROR, interruptedDetail, "")`
   for it, then destroy the sandbox named by its `TaskStarted.sandbox_id` (if
   any; `context.WithoutCancel`; a failure — usually "not found" — only prints
   the destroy line), where

   ```go
   // interruptedDetail is the task_finished detail Resume writes for a run that was in progress when the engine stopped (m3.md decision 3).
   const interruptedDetail = "interrupted"
   ```

7. `f.git.restore(ctx, lastCommit(events))` (after step 6 the new event does
   not change it); also `removeUnder(dir, dir/incoming)` and `dir/previous`.
8. Print `workflow <id>: resumed`; `return e.drive(ctx, f)`.

**Why an interrupted run is closed as `ERROR` and redone, never continued
(m3.md decision 3).** Continuing the child (harness `Resume`) would need a
live harness process attached to its sandbox and a run log that ends at a
verified checkpoint; neither is guaranteed after a crash, and the engine's
own post-run steps (collect, commit, checks) may have been half done.
Redoing the attempt from the last committed
workspace is always possible and always safe: the workspace on the host is
only ever replaced by a completed swap followed by a commit, and step 7
rebuilds it from the last commit the log knows about, discarding any later
orphan commit or half-written `incoming/`. Cost: the interrupted run's work
(at most one task's run, decision 3). The orphaned child run is left as it
is; its sandbox is destroyed in step 6 (it is named in `TaskStarted.sandbox_id`).

Resume after a crash at each point:

| Last event in the log | What Resume does |
|-----------------------|------------------|
| `workflow_started` | runs the first task |
| `task_started` (crash during the run, collect or checks) | step 6 writes `task_finished{ERROR, "interrupted"}`; `next` starts the same attempt as a new run (next run id) |
| `task_finished{PASSED}` | next task (or `workflow_completed{COMPLETED}` if it was the last) |
| `task_finished{CHECKS_FAILED/RUN_FAILED}` | next attempt, or `workflow_completed{FAILED}` when attempts are used up |
| `task_finished{ERROR}` | same attempt, new run id |
| `task_finished{AWAITING_APPROVAL}` | child still paused → parks again (no event); child finished → `execContinue` (new `task_started`, same run id, copy the kept sandbox out) |
| `workflow_completed` | `ErrWorkflowFinished` |

A crash between `collect`'s commit and `task_finished` leaves a commit the
log does not name; step 7 resets the branch to the last recorded commit, so
the orphan commit is simply unreachable.

### Progress lines (to `Options.Stdout`, each ending in `\n`)

| When | Line |
|------|------|
| Start, workspace made | `workflow <id>: workspace from <repo> at <head> (committed files only)` (only with a repo) |
| Start, after `workflow_started` | `workflow <id>: started, workspace <ws>` |
| Resume | `workflow <id>: resumed` |
| `task_started` | `task <task>: <role> attempt <a>[ round <r>] started, run <run-id>` |
| `task_finished` | `task <task>: <role> attempt <a>[ round <r>] <outcome>[: <first line of detail>]` |
| `.git` dropped | `task <task>: dropped .git: <path>, <path>` |
| park | `workflow <id>: waiting for approval of run <run-id>` |
| `workflow_completed` | `workflow <id>: <outcome>[: <detail>]` |

`<role>` is `producer` / `critic` / `export`; `<outcome>` is the enum name
without its prefix (`TASK_OUTCOME_` / `WORKFLOW_OUTCOME_`), lower case,
`_` → space (`passed`, `checks failed`, `budget exhausted`). The detail is
omitted for `passed` and `completed`. `firstLine(s)` is `s` up to the first
`\n`.

### Hooks for W.4 and W.5

Put these in `engine.go` in one block that starts with the comment
`// ---- Hooks: tasks W.4 (review) and W.5 (gates, export) replace these. ----`.
Each is a no-op or a clear error, so W.3 compiles and runs workflows
without review, gates or export. **W.4 deletes the four review stubs and
implements them in `review.go`; W.5 deletes the two gate/export stubs and
implements them in `gate.go` / `export.go`.** Nothing else in `engine.go`
needs to change for them.

```go
// producerRound is the review round of t's next producer run: 0 without review.
// W.3: review is not implemented, so always 0 (a task with review: is run without one).
func (e *Engine) producerRound(t Task, p taskProgress) uint32 { return 0 }

// reviewNote is the reviewer's feedback appended to a producer prompt in round > 1.
// W.3: no review, "".
func (e *Engine) reviewNote(ctx context.Context, t Task, p taskProgress, round uint32) (string, error) { return "", nil }

// nextReviewAction decides the next step of a task with review (task W.4).
func (e *Engine) nextReviewAction(ctx context.Context, f *flow, st *State, events []*adlcv1.Event, t Task, p taskProgress) (action, error) {
	return action{}, fmt.Errorf("workflow: task %s: review is not implemented (task W.4)", t.ID)
}

// finishCritic finishes a critic run (task W.4).
func (e *Engine) finishCritic(ctx context.Context, f *flow, t Task, rec *runRecord, h sandbox.Handle, state *eventlog.RunState, runErr error) error {
	return fmt.Errorf("workflow: task %s: review is not implemented (task W.4)", t.ID)
}

// checkGate runs before a task's first run, also on Resume (task W.5).
// park = stop driving and return the state. W.3: no gates.
func (e *Engine) checkGate(ctx context.Context, w *Workflow, st *State, t Task) (park bool, err error) { return false, nil }

// runExport runs a kind: export task (task W.5): task_started, the export, task_finished.
func (e *Engine) runExport(ctx context.Context, w *Workflow, st *State, t Task) error {
	return fmt.Errorf("workflow: task %s: kind export is not implemented (task W.5)", t.ID)
}
```

Helpers W.4 and W.5 will call (keep their names and signatures):
`progressOf`, `taskProgress` methods, `runRecord`, `runSpec`, `action` and its
kinds, `e.producerRun`, `e.execRun` (role-generic), `e.parkedAction`,
`e.finishTask`, `e.add`, `e.appendEvent`, `e.fold`, `e.newFlow`, `e.drive`,
`e.spent`, `nestBudget`, `childRunID`, `nextRunNumber`, `e.finishTaskKeep`, `f.git` (`diff`, `run`), `flowSources`,
`firstLine`, `removeUnder`.

### `interfaces.md` text

Append at the end of §12 (W.1 creates §12; do not create or reorder it):

```markdown
### W.3 — engine (`internal/workflow`)

`TaskRun`, `Options` (`Log`, `Runs`, `Sandbox`, `Image`, `Env`, `BaseDir`, `RunTask`,
`AgentBudget`, `Now`, `Stdout`; `Gates` is added by W.5, `SkipExport` by W.6), `Engine`,
`New`, `Start`, `Resume`, `Workspace`; errors `ErrWorkflowExists`, `ErrWorkflowNotFound`,
`ErrWorkflowChanged`, `ErrWorkflowFinished`. Layout `<BaseDir>/<id>/{workspace,git,inputs}`;
all host git with `--git-dir`, hooks and fsmonitor off, no inherited `GIT_*`, identity
`adlc <adlc@localhost>`. Child run ids `<workflow-id>-<n>`; `sandbox_id` in `task_started`
and in a parked `task_finished`. `ERROR` (also `"interrupted"` on Resume) repeats the same
attempt as a new run. Checks: `sh -c`, fresh sandbox, first failure stops, 10 min each.
Evals: `evals.GoEnv() map[string]string` (copy of the eval sandbox environment).
`internal/evals` never imports `internal/workflow`.
```

## Steps

1. Branch from the latest `main` (W.1 and W.2 merged). Read W.1/W.2's code:
   confirm `Parse`, `Order`, `Render`, `Fold`, `State`, `TaskState`,
   `WithFirstKind` and the `adlcv1` names (`adlcv1.TaskRole_TASK_ROLE_PRODUCER`,
   `adlcv1.Event_TaskStarted{TaskStarted: …}`, …) exist as m3.md says. If any
   differs, stop and report.
2. `evals.GoEnv`.
3. `workspace.go`: `runGit`, `gitRepo`, `removeUnder`, `dropGit`; `workspace_test.go` git tests.
4. `history.go` + `history_test.go`.
5. `budget.go` + `budget_test.go` (unit tests of `nestBudget`, `minPositive`).
6. `checks.go` + `checks_test.go` (unit tests of `tail`, `checkDetail`).
7. `engine.go`: types, `New`, `childRunID`, `commitMessage`, `add`, `drive`,
   `next`, `execRun`, `afterRun`, `finishProducer`, `finishTask`, hooks; `Start`.
8. `sources.go`; `helpers_test.go`; `engine_test.go`, `sources_test.go`.
9. Workspace creation and `collect`; `security_test.go`.
10. `Resume`, `execContinue`, `parkedAction`; `resume_test.go`.
11. `make fmt && make ci`. Break each piece once (see Tests) and see the test fail.

## Tests

All tests: `t.TempDir()` for everything; skip with `t.Skip("git not on PATH")`
when `exec.LookPath("git")` fails. No network, no model, no Docker.

### Helpers (`helpers_test.go`)

```go
// clock is a test clock; tick advances it. Pass clock.now to Options.Now and
// eventlog.WithClock for both stores.
type clock struct{ mu sync.Mutex; t time.Time }

// testEnv is one engine on fresh stores.
type testEnv struct {
	eng    *Engine
	log    *eventlog.Store // workflows.db, WithFirstKind("workflow_started")
	runs   *eventlog.Store // events.db
	host   *sandboxtest.Host
	rec    *recordingSandbox // wraps host; records Create specs and Destroy ids
	fake   *fakeRunner
	base   string            // BaseDir
	out    *bytes.Buffer     // Stdout
	clock  *clock
}
func newTestEnv(t *testing.T) *testEnv
// reopen returns a second engine on the same base dir, run store and sandbox
// but on the workflow log log (used after truncateLog).
func (te *testEnv) reopen(t *testing.T, log *eventlog.Store) *Engine

// makeRepo creates a git repository with files committed (identity t <t@t>).
func makeRepo(t *testing.T, files map[string]string) string

// parseWorkflow is workflow.Parse of a YAML string with dir = t.TempDir().
func parseWorkflow(t *testing.T, yaml string) *Workflow

// fakeStep is what the fake RunTask does on one call.
type fakeStep struct {
	sh     string            // run with sh -c in the sandbox workspace first ("" = nothing); a non-zero exit fails the test (t.Errorf)
	stop   adlcv1.StopReason // 0 = COMPLETED
	result string
	usd    int64             // cost of the run (on run_completed)
	tokens int64             // input tokens of the run (on run_completed)
	err    error             // returned instead of a state; nothing is written to Runs
	hook   func(tr TaskRun)  // called first, e.g. to cancel a context
}

// fakeRunner is Options.RunTask. Steps are consumed in order per task id.
// It records every TaskRun and writes a real child run to Runs:
// run_started{agent_version: tr.AgentRef, task: tr.Prompt, budget: tr.Budget,
// workflow_id, task_id}, for AWAITING_APPROVAL also approval_requested{approval_id "a-1"},
// then run_completed{stop, result} with Cost{InputTokens: tokens, UsdMicros: usd}.
type fakeRunner struct {
	t     *testing.T
	runs  *eventlog.Store
	mu    sync.Mutex
	steps map[string][]fakeStep
	calls []TaskRun
}
func (f *fakeRunner) RunTask(ctx context.Context, tr TaskRun) (*eventlog.RunState, error)

// truncateLog copies the first n events of workflow id into a new store
// (Append with each event's Seq) — the log as a crash after event n leaves it.
func truncateLog(t *testing.T, src *eventlog.Store, id string, n int) *eventlog.Store

// kinds returns eventlog.Kind of each event of workflow id.
func kinds(t *testing.T, log *eventlog.Store, id string) []string
```

`AgentBudget` in `newTestEnv` returns `&adlcv1.Budget{MaxSteps: 30}` for any
ref and records the refs it was asked for. Workflow id in tests:
`"t-v1-20261114T101500Z-abcdef"` (decision 14 format); child runs are `…-abcdef-1`, `-2`, ….

### `workspace_test.go`

| Test | Asserts |
|------|---------|
| `TestRunGitIgnoresUserAndSystemConfig` | `t.Setenv("GIT_CONFIG_GLOBAL", f)` with `f` = `[core]\n\tfsmonitor = touch <marker>\n[user]\n\tname = Someone\n`, also `t.Setenv("GIT_DIR", "/nonexistent")`; `init`, write a file, `commit` → marker does not exist; `git log -1 --format=%an <%ae>` (through `runGit`) is `adlc <adlc@localhost>` |
| `TestCommitIsDeterministic` | two repos, same files, same clock → same commit hash |
| `TestCommitCleansIgnoredFiles` | workspace with `.gitignore` = `out/` and `out/x` → after `commit` `out/` is gone and `ls-tree` has `.gitignore` only |
| `TestCommitAllowsEmpty` | two commits without changes → two different hashes |
| `TestRestoreRebuildsWorkspace` | commit A (file a), commit B (a changed, b added); scribble an extra file; `restore(A)` → workspace has exactly `a` with A's content; `rev-parse HEAD` is A |
| `TestRemoveUnderRefusesOutside` | `removeUnder(base, base)`, `removeUnder(base, filepath.Dir(base))`, `removeUnder(base, base+"/../x")` → each the error, nothing removed |
| `TestDropGitAtAnyDepth` | tree with `.git/config`, `sub/.git/HEAD`, `other/.git` (a *file*), `x/.GIT/` → all four gone, sorted list `[.git other/.git sub/.git x/.GIT]`; normal files untouched |
| `TestStartWorkspaceHasCommittedFilesOnly` | repo with committed `a.txt` = `v1`, then uncommitted `a.txt` = `v2`, untracked `n.txt`; Start a one-task workflow (fake does nothing) → workspace `a.txt` = `v1`, no `n.txt`, no `.git`; `workflow_started.base_commit` = first commit of the engine git dir (message `base`); `workflow_started.source_commit` = the repo's HEAD; Stdout has the `committed files only` line |
| `TestStartWithoutRepoInput` | workflow without inputs → empty workspace, base commit exists, `source_commit == ""` |
| `TestStartRejectsBadRepo` | `repo` dir without `.git` → error `is not a git repository with a .git directory`; repo with `.git` but no commit → `has no commit`; both: no `<BaseDir>/<id>` left, log empty |
| `TestStartValidatesInputs` | table: missing input, unknown input, file input pointing at a dir, `repo` declared as kind `file` → exact errors; nothing written |
| `TestFileInputIsSnapshotted` | `issue` file input; Start with a task that fails with an `ERROR` (fake step `err`); change the file on disk; Resume → the second `TaskRun.Prompt` (rendering `{{ input "issue" }}`) has the original content |

### `security_test.go`

| Test | Asserts |
|------|---------|
| `TestAgentGitDirNeverRunsOnHostAndIsNotCommitted` | The fake's step (run in the sandbox workspace, which `sandboxtest.Host` puts on the host — the worst case) writes: `.git/config` = `[core]\n\tfsmonitor = touch <m1>\n\thooksPath = .git/hooks\n[filter "evil"]\n\tclean = touch <m2>\n`, an executable `.git/hooks/pre-commit` = `#!/bin/sh\ntouch <m3>\n`, `sub/.git/config` with `fsmonitor = touch <m4>`, a file `other/.git` = `gitdir: <tmp>/evil`, `.gitattributes` = `* filter=evil\n`, and a normal change `a.txt` = `changed`. The task has check `test -f a.txt`. → none of `m1`…`m4` exists; `task_finished` is `PASSED` with a commit; `git ls-tree -r --name-only <commit>` has `.gitattributes` and `a.txt` and no path with a `.git` component (case-insensitive); the workspace has no `.git` anywhere; Stdout has `dropped .git: .git, other/.git, sub/.git` |
| `TestNextSandboxGetsNoAgentGit` | task 1's fake creates `.git/config`; task 2's fake step is `test ! -e .git` (passes only if the next sandbox has no `.git`) |

Break the drop (skip `dropGit`) and see the first test fail; restore.

### `history_test.go`

| Test | Asserts |
|------|---------|
| `TestProgressOfPairsStartedAndFinished` | hand-built events (two tasks interleaved, a retry, an ERROR) → records per task in order with the right `Finished`, `StartedAt`, `FinishedAt` |
| `TestProgressBaseCommit` | base `B`, task x finished with `C1`, task y started → y's `BaseCommit == "C1"`; x's `== "B"` |
| `TestFailedInRound` | counts only PRODUCER CHECKS_FAILED/RUN_FAILED of the round (not ERROR, not AWAITING_APPROVAL, not other rounds) |
| `TestNextRunNumber` | runs `-1` (parked), `-1` (continuation), `-2` (ERROR), export with `""` → 3 |
| `TestLastCommitSkipsEmpty` | `task_finished` with `commit: ""` after one with `C1` → `C1` |

### `budget_test.go`

| Test | Asserts |
|------|---------|
| `TestNestBudget` | table. (a) agent `{steps 30, usd 1_000_000, wall 10m, tool calls 50}`, task `{usd 500_000}`, wf `{usd 2_000_000, tokens 1000}`, used `{usd 1_800_000, tokens 100}` → `{steps 30, tool calls 50, usd 200_000, tokens 900, wall 10m}`; (b) nil agent, no limits → `&adlcv1.Budget{}` with nil wall clock; (c) wf wall 1h, used 50m, agent wall 0 → wall 10m; (d) used usd == wf usd → nil and `max_usd_micros 2000000 reached (used 2000000)`; (e) wall and usd both spent → the wall-clock detail (order); (f) tokens spent → `max_tokens …` |
| `TestNestBudgetDoesNotChangeAgentBudget` | the agent budget passed in is unchanged afterwards (`proto.Equal` with a copy) |
| `TestSpentSumsChildRuns` | two child runs with costs (in, out, cache read, cache write, usd) and one run id that does not exist → sums all four token kinds and usd; missing run skipped |
| `TestSpentWallClockIsActiveTime` | events: task A 10:00:00–10:05:00, AWAITING 10:05:00, continuation 12:00:00–12:01:00, interrupted run started 13:00:00 whose child's last event is 13:02:00 (closed ERROR at 15:00:00) → 5m + 1m + 2m = 8m |
| `TestWorkflowBudgetStopsBeforeTask` | wf `max_usd: 0.10`; task 1's fake costs 150_000 µ$ → task 2 never starts (no second `task_started`, fake not called); `workflow_completed{BUDGET_EXHAUSTED, "workflow budget: max_usd_micros 100000 reached (used 150000)"}` |
| `TestTaskStartedBudgetIsNested` | task `budget: {max_usd: 0.50}`, agent 1.00, wf 2.00 → `task_started.budget.max_usd_micros == 500000`, `max_steps == 30`, and the `TaskRun.Budget` the fake got is `proto.Equal` to it |

### `checks_test.go`

| Test | Asserts |
|------|---------|
| `TestTailKeepsRuneBoundary` | `"x" + "é" + 4095 × "a"` (4098 bytes) with max 4096 → the cut lands on the second byte of `é`, which is dropped: result is exactly 4095 × `a`, `cut == true`, valid UTF-8; a 10-byte input → unchanged, `cut == false` |
| `TestCheckDetail` | exit 1, output `boom\n` → exact `check 2 of 3 failed: go test ./...\nexit code 1\noutput:\nboom`; timed out → second line `exit code -1, timed out after 10m0s`; 10 000 bytes → header `output (last 4096 bytes):` and the last 4096 bytes; empty output → `(no output)` |
| `TestChecksStopAtFirstFailure` | checks `true`, `echo out; echo err >&2; exit 3`, `touch <marker>` → `CHECKS_FAILED`, detail is `check 2 of 3 failed: …\nexit code 3\noutput:\nout\nerr` (order kept by `exec 2>&1`); marker does not exist |
| `TestChecksRunInFreshSandbox` | `recordingSandbox` → the check sandbox's spec has `RunID == <run id>.check` and `WorkspaceDir == Workspace(id)`, a different handle from the run's, and it is destroyed |
| `TestNoChecksNoSandbox` | a task without checks → exactly one `Create` (the run's) |

### `engine_test.go`

| Test | Asserts |
|------|---------|
| `TestNewValidatesOptions` | table: each required field missing → the exact error; defaults: `Image == evals.DefaultImage`, `Env` equals `evals.GoEnv()`, `BaseDir` absolute |
| `TestChildRunID` | `childRunID("t-v1-20261114T101500Z-abcdef", 12) == "t-v1-20261114T101500Z-abcdef-12"` |
| `TestCommitMessage` | `("x", 2, 0)` → `task x attempt 2`; `("x", 3, 2)` → `task x attempt 3 round 2` |
| `TestStartRunsTasksInOrder` | tasks in file order `c` (depends on `a`), `a`, `b` (depends on `c`) → fake called for `a`, `c`, `b`; kinds exactly `workflow_started, task_started, task_finished, task_started, task_finished, task_started, task_finished, workflow_completed`; `workflow_completed.outcome == COMPLETED`; returned `State.Status == "completed"`, every `TaskState.Passed` |
| `TestTaskStartedAndTaskRunFields` | `task_started{task_id, role PRODUCER, attempt 1, round 0, run_id "t-v1-20261114T101500Z-abcdef-1", sandbox_id == TaskRun.Handle.ID}`; `TaskRun.{RunID, WorkflowID, Task.ID, Role, AgentRef == Task.Agent, Prompt == rendered, Sandbox != nil, Handle.ID != ""}`; the run's sandbox spec has `RunID == run id`, `CPUs 2`, `MemoryBytes 4<<30`, `Network false` |
| `TestTaskFinishedHasCommit` | the commit in `task_finished` is the engine git dir's `HEAD`, message `task a attempt 1`, and its tree has the file the fake wrote |
| `TestCopyOutReplacesWorkspace` | fake deletes `old.txt` and adds `new.txt` → workspace has `new.txt`, not `old.txt`; no `incoming/` or `previous/` left |
| `TestChecksFailThenRetryPasses` | `attempts: 2`, check `test -f done.txt`; step 1 does nothing, step 2 creates `done.txt` → outcomes `CHECKS_FAILED`, `PASSED`; attempt 2's run id `…-2`; its prompt == rendered prompt + `fmt.Sprintf(retryNoteFormat, 1, <attempt 1 detail>)` (compare the whole string); commits `task t attempt 1`, `task t attempt 2`, the second's parent is the first; `COMPLETED` |
| `TestChecksFailAfterAllAttempts` | `attempts: 2`, both fail → `workflow_completed{FAILED, "task t failed after 2 attempt(s): check 1 of 1 failed: test -f done.txt"}`; the next task never starts |
| `TestRunFailedIsRetriedWithoutChecks` | step 1 `stop: BUDGET_EXHAUSTED` (stop detail from run_completed `max_steps 30 reached`), check `false` → attempt 1 `RUN_FAILED` with detail `run stopped: BUDGET_EXHAUSTED: max_steps 30 reached` and a commit (checks not run: otherwise it would be `CHECKS_FAILED`) |
| `TestRunTaskErrorLeavesWorkflowRunning` | step 1 `err: errors.New("docker down")` → `Start` returns an error wrapping it; last event `task_finished{ERROR, "run task: docker down"}`; no `workflow_completed`; `Fold` status `running`; then `te.eng.Resume` with a good step → new run id `…-2`, `attempt == 1`, `COMPLETED` |
| `TestRenderErrorFailsWorkflow` | prompt `{{ file "missing.md" }}` → `workflow_completed{FAILED}` with detail prefix `task t: render prompt: ` and containing `not found in the workspace`; no `task_started` |
| `TestChildParksWorkflow` | step `stop: AWAITING_APPROVAL` → `Start` returns `nil` error and `State.Status == "awaiting_approval"`; last event `task_finished{AWAITING_APPROVAL}` with `sandbox_id` = the run's sandbox (same as `task_started.sandbox_id`) and `commit == ""`; that sandbox is not destroyed; Stdout has `waiting for approval of run …` |
| `TestResumeWhileChildStillPausedParksAgain` | after the previous: `Resume` → no new events, status `awaiting_approval` |
| `TestResumeAfterChildApprovedCollectsIt` | after the park: in the kept sandbox run `sh -c 'echo approved > b.txt'` (`te.host.Exec`), append `approval_resolved{a-1, approved}` and `run_completed{COMPLETED, "done"}` to the child run; `Resume` → appended `task_started` (same run id `…-1`, attempt 1, same `sandbox_id`) and `task_finished{PASSED}` whose commit has `b.txt`; the sandbox is destroyed; `COMPLETED`; the fake was called once in total |
| `TestResumeWhileChildRunningFails` | parked child whose log then gets `approval_resolved` only (running) → `Resume` error contains `is still running` |
| `TestCancelledContextCancelsWorkflow` | step `hook` cancels the context passed to `Start` and returns state `CANCELLED` → `task_finished{ERROR, "cancelled"}`, `workflow_completed{CANCELLED, "cancelled"}`; `Start` returns the state and nil |
| `TestSandboxesAreDestroyed` | after a completed two-task workflow with checks, the `sandboxtest.Host` dir has no `sb-*` directories |
| `TestStartRefusesExisting` | second `Start` with the same id → `errors.Is(err, ErrWorkflowExists)`; a leftover `<BaseDir>/<id>` without a log → `ErrWorkflowExists` and the dir is still there |
| `TestConcurrentWriterIsDetected` | append any valid event to the workflow log from the test between two engine appends (fake step `hook` does it) → the engine's next `add` fails with an error wrapping `eventlog.ErrSeqConflict` |
| `TestProgressLines` | Stdout of a one-task run contains exactly the lines `workflow <id>: started, workspace <ws>`, `task a: producer attempt 1 started, run <run id>`, `task a: producer attempt 1 passed`, `workflow <id>: completed` |

### `sources_test.go`

| Test | Asserts |
|------|---------|
| `TestPromptSources` | inputs `issue` (file, `Fix the bug`), `note` (text, `be brief`); task `spec` writes `spec.md` = `# Spec\n`, result `spec done`; task `impl` prompt `{{ input "issue" }}|{{ input "note" }}|{{ file "spec.md" }}|{{ result "spec" }}|{{ diff "spec" }}` → `TaskRun.Prompt` starts with `Fix the bug|be brief|# Spec\n|spec done|diff --git a/spec.md b/spec.md` and contains `+# Spec` |
| `TestSourcesErrors` | `Input("nope")`, `Input("repo")` (dir), `File("../x")`, `File("missing")`, `Result("later")`, `Diff("later")` → the exact errors of the table |

### `resume_test.go`

Each test runs a whole workflow first, then cuts the log with `truncateLog`
to simulate a crash after event *n*, then `te.reopen(t, cut).Resume`.

| Test | Asserts |
|------|---------|
| `TestResumeUnknownWorkflow` | `ErrWorkflowNotFound` |
| `TestResumeRefusesFinished` | completed workflow → `errors.Is(err, ErrWorkflowFinished)`, state returned with `Outcome == COMPLETED` |
| `TestResumeRefusesChangedWorkflow` | `Resume` with a workflow whose `Digest` differs → `ErrWorkflowChanged`; nothing appended |
| `TestResumeAfterCrashDuringTask` | two tasks; cut after task 2's `task_started` → appended exactly `task_finished{ERROR, "interrupted", run id …-2}`, `task_started{attempt 1, run id …-3}`, `task_finished{PASSED}`, `workflow_completed{COMPLETED}`; task 1 not re-run |
| `TestResumeAfterCrashBetweenTasks` | cut after task 1's `task_finished` → only task 2 runs, and its sandbox sees task 1's file (task 2's fake step is `test -f one.txt`) |
| `TestResumeAfterCrashBeforeWorkflowCompleted` | cut only the last event → Resume appends `workflow_completed{COMPLETED}` and calls the fake 0 times |
| `TestResumeRestoresWorkspaceToLastRecordedCommit` | cut after task 1's `task_started` (the full run had already committed task 1 and 2 and replaced the workspace); also create `incoming/junk` → before the redo, the workspace equals the base commit (fake step of the redo checks `test ! -e one.txt`), `incoming/` is gone, and the final commit's parent is the base commit (the orphan commits are unreachable) |
| `TestResumeAfterCrashKeepsRetryNote` | `attempts: 2`; cut after attempt 2's `task_started` → the redo (a new run id, attempt 2) gets the same prompt as the original attempt 2 (retry note from attempt 1) |

## Verify

```bash
go test ./internal/workflow/ -run 'Git|Commit|Restore|Remove|DropGit|Start|Agent|NextSandbox' -v   # PASS
go test ./internal/workflow/ -run 'Progress|Failed|LastCommit|Nest|Spent|Budget|Tail|Check|NoChecks' -v
go test ./internal/workflow/ -run 'New|ChildRunID|Task|Copy|Retry|Run|Render|Park|Resume|Cancel|Sandboxes|Refuses|Concurrent|Sources' -v
go test -race ./internal/workflow/ ./internal/evals/                                              # PASS
make ci                                                                                           # passes
```

## Out of scope

- The CLI (`adlc workflow run/resume/approve/status`), the real `RunTask` and
  `AgentBudget` wiring, `defaultWorkflowDBPath` (task W.5).
- Setting `RunStarted.workflow_id` / `task_id` in child runs: the harness has
  no fields for them yet; the CLI wiring adds that (see the PR note).
- Review rounds and the critic (task W.4); gates and export (task W.5);
  `Options.Gates`, `Options.SkipExport`.
- Parallel tasks, per-task workspaces, merges (m3.md decision 5).
- Continuing an interrupted child run with `harness.Resume`; marking the
  orphaned child run itself as finished.
- Any change to `events.proto`, `Fold` or `internal/harness`.

## Pitfalls

- Every host git command goes through `runGit`. Never `exec.Command("git", …)`
  anywhere else, never `git -C <workspace>`, never a git command without
  `--git-dir` on the workspace. A test that passes with plain `git` in the
  workspace proves nothing; the security test uses `sandboxtest.Host`, which
  runs the agent's commands on the host, on purpose.
- Drop `.git` **before** the first git command on the new tree, at every
  depth, files as well as directories.
- `git archive` of `HEAD` (a commit) adds `pax_global_header`; archive `<head>^{tree}`.
- `os.RemoveAll` only through `removeUnder(f.dir, …)`; never on `BaseDir`
  itself, never on a path you did not build from `f.dir`.
- `BaseDir` must be absolute (New does it); `--git-dir` paths are used with
  `cmd.Dir` set to the flow directory.
- Always append with `context.WithoutCancel(ctx)` and an explicit `Seq`; set
  `Time` from `Options.Now`. Tests use the same `clock` for both stores and
  `Options.Now`.
- Do not destroy a parked run's sandbox. Do destroy every other sandbox,
  also on error paths (`defer` with a `keep` flag).
- `Runs.State` of a run that does not exist returns `eventlog.ErrRunNotFound`:
  that is normal after a `RunTask` error (skip it in `spent`).
- `next` must be decided from the log only (plus child states and the
  workspace). Do not keep counters in the `Engine` or `flow` between loop
  iterations; Resume would lose them.
- The workflow log is opened `WithFirstKind("workflow_started")`; never call
  `Log.State` on it (that is the run fold). Use `workflow.Fold`.
- `internal/evals` must not import `internal/workflow`.
- `sandboxtest.Host.Exec` runs on the host; in fake steps use relative paths
  for workspace files and absolute paths (in `t.TempDir()`) only for markers.

## Done checklist

- [ ] `New`, `Start`, `Resume`, `Workspace`, `TaskRun`, `Options` (without `Gates`/`SkipExport`) and the four errors exist exactly as in m3.md
- [ ] Workspace from the committed tree of `repo`; file inputs snapshotted; `source_commit` recorded
- [ ] All host git through `runGit` (own config only, hooks and fsmonitor off, identity `adlc <adlc@localhost>`)
- [ ] Every `.git` dropped before commit; security tests pass and were seen failing
- [ ] Budgets nested as specified; workflow budget checked before every run
- [ ] Checks in a fresh sandbox, first failure stops, detail format exact
- [ ] Retries with the exact retry note; `ERROR` does not use up an attempt
- [ ] Child run ids `<workflow-id>-<n>` from the log; `sandbox_id` in `task_started` and in a parked `task_finished`
- [ ] Parking keeps the sandbox; Resume continues with the same run id and collects the run once it finished
- [ ] Resume closes an interrupted run, restores the workspace from the last recorded commit and continues
- [ ] Hook block for W.4/W.5 present and marked
- [ ] `evals.GoEnv` added; §12 subsection "W.3 — engine" appended; PR notes the shared files and the `evals` → `workflow` import rule
- [ ] `make ci` passes
