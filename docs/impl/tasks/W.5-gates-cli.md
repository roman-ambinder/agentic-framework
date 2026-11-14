# W.5 — Gates, export and `adlc workflow`

Size: M · Depends on: W.3 · Branch: `task/W.5-gates-cli` · PR title: `W.5: human gates, export to a local branch and adlc workflow`

## Goal

After W.3 the engine runs a workflow's tasks, but a task with `gate: approval`
starts without asking anyone, a task of `kind: export` does nothing, and there
is no command to run a workflow. After this task:

- a task with `gate: approval` **parks** the workflow before it starts
  (`gate_requested`, status `awaiting_approval`, the process exits 0), and
  `Engine.ResolveGate` records `gate_resolved` and continues or ends the
  workflow (m3.md decision 10);
- `Options.Gates = GateAutoApprove` (used by pipeline evals, W.6) writes the
  request and an automatic approval and goes on;
- a task of `kind: export` creates the local branch `adlc/<workflow-id>` in the
  repository named by the input `repo`, with the workflow's change, using
  host-side git with hooks disabled; it never pushes and never touches the
  person's working tree, index or current branch (m3.md decision 11);
- `adlc workflow run|resume|approve|status` exist (m3.md "CLI") and wire the
  engine to the real harness the way `adlc eval` does: sandbox tools, the kill
  switch, `NAME@N`/`NAME@ENV` agent references, nested budgets, and child
  runs whose `run_started` carries `workflow_id` and `task_id`;
- a child run that parked for a tool approval (task C.1) can be answered with
  `adlc approve`, which continues it on the sandbox the engine kept for it;
  `adlc workflow resume` then collects it (m3.md decision 10, second half).

`harness.Config` has no way to set `RunStarted.workflow_id` and `task_id`
today (`internal/harness/loop.go` writes only `agent_version`, `task`,
`budget`). This task adds two fields, additively. No schema change: both
proto fields exist (`events.proto`, `RunStarted` fields 4 and 5) and
`eventlog.Fold` already copies them into `RunState.WorkflowID` / `TaskID`.
No new dependencies.

## Read first

- `AGENTS.md`; `docs/impl/m3.md` (all of it: it is the contract; decisions 2, 4, 7, 10, 11 and "The API" matter most)
- `docs/impl/tasks/W.1-workflow-file.md`, `W.2-workflow-events.md`, `W.3-workflow-engine.md` (what already exists; the helper names W.3 chose)
- `docs/impl/interfaces.md` §6 (harness `Config`, `Deps`, `KillSwitch`), §7 (CLI wiring), §8 (sandbox, sandbox tools), §11 (C.1 approvals, C.3 kill switch, C.4 registry)
- `docs/impl/tasks/C.1-approvals.md` (CLI conventions: `approverName`, exact output lines, exit codes)
- `internal/workflow/engine.go` (W.3) — find the two places marked for W.5: the gate check before each task and the export branch
- `internal/harness/harness.go` (`Config`), `loop.go` (`run`: the `run_started` event), `fork.go` (writes its own `run_started`; not changed)
- `internal/cli/eval.go` (`evalRunAgent`: how a run gets the sandbox tools), `run.go` (`runOptions`, `loadAgent`, `runAgentWith`), `resume.go`, `approve.go` (`approverName`, `approveRun`), `agentref.go` (`resolveAgentRef`), `controldb.go` (`openKillSwitch`, `defaultControlDBPath`), `wiring.go` (`buildModel`, `buildPlanModel`, `harnessConfig`, `startHooks`), `openlog.go` (`openExistingLog`, `defaultDBPath`), `summary.go` (`formatUSD`), `exit.go`, `root.go`, `cli_test.go`, `approve_test.go`
- `internal/evals/runner.go` (`changedFiles`, `gitOutput`: why every host git call disables hooks and fsmonitor)
- `internal/sandbox/sandboxtest/host.go` (`NewHost`: the test sandbox)

## Files

Create:

```
internal/workflow/gate.go           checkGate, ResolveGate, ErrNoPendingGate, gate summary
internal/workflow/gate_test.go
internal/workflow/export.go         runExport, ExportInput, repoGit
internal/workflow/export_test.go
internal/harness/workflow_ids_test.go
internal/cli/workflow.go            the four commands and the engine wiring
internal/cli/workflow_print.go      run summary, status list, status detail
internal/cli/workflow_test.go
internal/cli/testdata/workflow/flow.yaml
internal/cli/testdata/workflow/writer.yaml
internal/cli/testdata/workflow/writer.fake.json
```

Change:

```
internal/workflow/engine.go         delete W.3's two W.5 stubs (checkGate, runExport) from the hook block;
                                    add the field Options.Gates
internal/workflow/validate.go       one more rule: an export task needs input "repo" of kind dir
                                    (the file where W.1 put Validate's rules; W.1 left this rule to W.5)
internal/workflow/validate_test.go  one row for that rule
internal/harness/harness.go         Config.WorkflowID, Config.TaskID
internal/harness/loop.go            write them into run_started
internal/cli/approve.go             continue a workflow child run on its kept sandbox (see "Tool approvals inside a workflow")
internal/cli/approve_test.go        tests for that
internal/cli/root.go                one AddCommand line                              (shared file)
internal/cli/cli_test.go            add the workflow commands to the shared-default tests
docs/impl/interfaces.md             §6 Config fields; the M3 section: gates, export, CLI (shared file)
```

Do not change `events.proto`, `internal/eventlog`, `fold.go` of `internal/workflow`
(W.2), `review.go` (W.4) or `internal/evals`. `Makefile` and
`.github/workflows/ci.yml` are not touched.

## Design

### Harness: workflow and task ids on `run_started`

`internal/harness/harness.go`, append to `Config` (after `Plan`):

```go
	// WorkflowID and TaskID go into run_started when the run is a task of a
	// workflow (task W.5); both "" otherwise.
	WorkflowID string
	TaskID     string
```

`internal/harness/loop.go`, `run()`: the `RunStarted` literal becomes

```go
&adlcv1.RunStarted{
	AgentVersion: l.cfg.AgentVersion, Task: l.cfg.Task, Budget: l.cfg.Budget,
	WorkflowId: l.cfg.WorkflowID, TaskId: l.cfg.TaskID,
}
```

Nothing else in the harness changes. `Fork` keeps writing its own
`run_started` without these fields; `Resume` and `Approve` never write
`run_started`. Every existing harness test passes unchanged.

### Where the two hooks go (`internal/workflow/engine.go`)

W.3's `engine.go` has a block that starts with
`// ---- Hooks: tasks W.4 (review) and W.5 (gates, export) replace these. ----`.
**Delete** its two W.5 stubs and implement them, with the same signatures,
in `gate.go` and `export.go`. Nothing else in W.3's loop changes:

```go
// gate.go
// checkGate runs before a task's first run, also on Resume. park == true
// means stop driving and return the state (W.3's drive folds the log again,
// because checkGate may have written events).
func (e *Engine) checkGate(ctx context.Context, w *Workflow, st *State, t Task) (park bool, err error)

// export.go
// runExport runs a kind: export task: task_started, the export,
// task_finished (PASSED or ERROR). It returns an error only when the
// workflow log cannot be written.
func (e *Engine) runExport(ctx context.Context, w *Workflow, st *State, t Task) error
```

What W.3's `drive`/`next` already do with them (do not change it): when
`checkGate` returns `false` but wrote events (an auto-approved gate), `next`
returns `actReload`; `park` → `actPark` with no run record, and `drive`
returns the folded state without printing. After `runExport`, `drive` folds
again: `PASSED` → next task; anything else → `next` writes
`workflow_completed{FAILED, "task <id> failed after 1 attempt(s): <first line of detail>"}`.
Export never retries (W.1 rule 18 forces `attempts: 1` on export tasks).

**`Options.Gates`.** W.3 does not add it (its brief leaves the type and the
field to W.5). Add, in `engine.go`'s `Options` after `Stdout`:

```go
	Gates GatePolicy // task W.5: GatePark (default) or GateAutoApprove (evals)
```

and in `gate.go`, exactly as m3.md:

```go
// GatePolicy says what a gate does when the workflow reaches it.
type GatePolicy int

// Gate policies.
const (
	GatePark        GatePolicy = iota // write gate_requested and stop
	GateAutoApprove                   // write gate_requested + gate_resolved{approved, by "auto"} and go on
)
```

If W.3 did declare them after all, keep W.3's declaration and do not add a second.

**W.3 helpers used here** (names from brief W.3; if the merged code differs,
use the merged names, never a second copy): `e.appendEvent(ctx, id, after, ev)`
(sets `run_id`, time and seq; refuses a concurrent writer), `e.fold(ctx, id)`
→ `(*State, []*adlcv1.Event, error)` (no events: `ErrWorkflowNotFound`),
`e.newFlow(w, id)` and `e.drive(ctx, f)` (continue a workflow),
`e.spent(ctx, events)` → `spend{USDMicros, Tokens, WallClock}`,
`lastCommit(events)` (the last commit recorded anywhere in the log), and
`f.git.run(ctx, args...)` → raw stdout of git on the workflow's own git
directory (`--git-dir`/`--work-tree` set, hooks off). Below, "append" means
`e.appendEvent(ctx, st.WorkflowID, <last seq>, ev)` with the seq of the event
appended just before (start with `st.LastSeq`).

### Gates (`internal/workflow/gate.go`)

```go
// ErrNoPendingGate means the task has no gate_requested without a gate_resolved.
var ErrNoPendingGate = errors.New("workflow: no pending gate for this task")

// ResolveGate records a person's answer to the pending gate of task taskID
// in workflow id and, when approved, continues the workflow like Resume. A
// rejection completes the workflow with outcome REJECTED.
func (e *Engine) ResolveGate(ctx context.Context, w *Workflow, id, taskID string, approved bool, by, comment string) (*State, error)
```

**`checkGate(ctx, w, st, t)`**, in this order:

1. `t.Gate != GateApproval` → `false, nil`.
2. `ts := st.Tasks[t.ID]`. `ts != nil && ts.GateApproved` → `false, nil` (approved earlier; this also covers a resume after a crash inside the task).
3. `ts == nil || !ts.GatePending` → append `gate_requested{task_id: t.ID, summary: gateSummary(...)}` (below) with `parent_seq` 0.
4. `e.o.Gates == GateAutoApprove` → append `gate_resolved{task_id: t.ID, approved: true, resolved_by: "auto", comment: ""}` with `parent_seq` = seq of that task's last `gate_requested` (the one just written, or the pending one from the log); return `false, nil`.
5. Otherwise (`GatePark`, and any unknown value) → `true, nil`. Nothing else is written; W.2's fold already gives status `awaiting_approval`.

A workflow parked at a gate and resumed (`Engine.Resume`) reaches step 3 with
`GatePending` true, so it parks again **without** a second `gate_requested`.
`checkGate` needs the events for the summary: read them with `e.fold(ctx, st.WorkflowID)`.

**Gate summary** — `gateSummary(ctx, w, st, events, t)` returns exactly four lines
joined with `"\n"` (no trailing newline):

```
workflow <workflow id> waits for approval before task "<task id>"
passed: <ids of passed tasks before t in w.Order(), ", "-separated; "-" when none>
changes: <git diff --shortstat output, trimmed; "none" when empty>
spent: $<usd with 6 decimals>, <tokens> tokens, <n> run|runs
```

- `changes`: `f := e.newFlow(w, st.WorkflowID)`; `f.git.run(ctx, "diff", "--shortstat", st.BaseCommit, lastCommit(events))`.
  When `lastCommit(events) == st.BaseCommit`, `none` without running git.
- `spent`: `sp := e.spent(ctx, events)`: `sp.USDMicros` formatted as
  `fmt.Sprintf("%d.%06d", m/1_000_000, m%1_000_000)` (the CLI's `formatUSD`,
  which this package cannot import) and `sp.Tokens` (the harness's own token
  count: input + output + cache read + cache write). `n` = the number of
  distinct non-empty `task_started.run_id` in `events`; `run` when n == 1, else `runs`.

Example (the test fixture below):

```
workflow flow@1-20261114T100000Z-0a1b2c waits for approval before task "release"
passed: write
changes: 1 file changed, 1 insertion(+)
spent: $0.001000, 150 tokens, 1 run
```

**`ResolveGate(ctx, w, id, taskID, approved, by, comment)`**:

1. `by == ""` → `errors.New("workflow: resolve gate: resolved by is empty")` (before reading anything).
2. `st, events, err := e.fold(ctx, id)` (no events → W.3's `ErrWorkflowNotFound` error).
3. `st.Digest != w.Digest` → the same `ErrWorkflowChanged` error W.3's `Resume` returns (same text).
4. `st.Status == "completed"` → the same `ErrWorkflowFinished` error W.3's `Resume` returns.
5. `ts := st.Tasks[taskID]`; `ts == nil || !ts.GatePending` →
   `fmt.Errorf("%w: task %q of workflow %q", ErrNoPendingGate, taskID, id)`. Nothing is appended in steps 1–5.
6. Append `gate_resolved{task_id: taskID, approved, resolved_by: by, comment}`,
   `parent_seq` = seq of that task's last `gate_requested` in `events`.
7. Not approved → append `workflow_completed{outcome: REJECTED, detail}` with
   `detail` = `gate of task <taskID> rejected by <by>` or, with a comment,
   `gate of task <taskID> rejected by <by>: <comment>`; return the folded state.
8. Approved → `return e.drive(ctx, e.newFlow(w, id))` (W.3's way to continue; the gate is now approved, so `checkGate` returns false and the task runs).

### Export (`internal/workflow/export.go`)

```go
// ExportInput is the input that names the repository an export task writes
// its branch into.
const ExportInput = "repo"
```

(W.3 has an unexported `repoInput = "repo"` for the same input; make it
`repoInput = ExportInput` so the two cannot drift.)

What W.3 guarantees about the workspace: it starts as `git archive` of the
**tree of `repo`'s HEAD at Start** (committed files only, no symlinks), and
the workflow's `base` commit message has a second paragraph
`source-commit: <that HEAD>`.

**Decision: branch from the commit the workflow started from, with the
workflow's diff applied in a private index.** The branch commit has
parent = the source commit and tree = the source commit's tree plus
`git diff <base> <final>` of the workflow git dir. The alternatives, and why not:

- *Fetch the workflow's final commit into the target repo.* It descends from
  the workflow's own root commit `base`, which shares no history with the
  person's repo: the branch would be unrelated history, useless for a pull
  request; and it copies another repository's objects into the person's repo.
- *`git commit-tree <final tree> -p <source>`.* Simple, but the final tree
  lacks what `git archive` and the extractor left out of the workspace
  (symlinks, submodule entries): the branch would delete them. Applying the
  diff keeps everything the tasks did not touch exactly as in the source commit.
- *Parent = the repo's current HEAD.* If HEAD moved since the start, the
  change was never built or tested on it; applying it there could conflict
  (export fails after a paid run) or silently combine with changes no check saw.

**What if the repo's HEAD moved since the start:** nothing special. The branch
starts at the source commit, which is exactly what the checks and the review
saw; the person merges or rebases it like any pull request branch. The
export says so in its detail (` (HEAD has moved since the workflow started)`).
If the source commit is no longer in the repo (history rewritten and pruned),
the export fails with E5 and creates nothing.

Final commit: `final := lastCommit(events)` (W.3). The export task is reached
only when every task before it in `w.Order()` passed, so the last recorded
commit is the last passed task's workspace (a failed attempt is always
followed by a later commit or by the end of the workflow).

**`runExport(ctx, w, st, t)`**:

1. Append `task_started{task_id: t.ID, role: TASK_ROLE_EXPORT, attempt: 1, round: 0, run_id: "", budget: nil}`.
2. `commit, detail, err := e.export(ctx, w, st, events, t)` (`events` from `e.fold`).
3. Append `task_finished{task_id: t.ID, role: TASK_ROLE_EXPORT, attempt: 1, round: 0, run_id: "",
   outcome: PASSED when err == nil else ERROR, detail: detail on success, err.Error() on failure,
   commit: the new branch commit ("" on failure)}`. `parent_seq` = the `task_started` seq.
4. Return only append errors.

**`e.export(ctx, w, st, events, t)`** — every step that fails returns the error
in the table below and creates nothing:

```
 1. repo := st.Inputs[ExportInput]; "" → E1.  abs := filepath.Abs(repo).
 2. top := repoGit(ctx, abs, nil, nil, "rev-parse", "--show-toplevel")  (fails → E2)
    filepath.EvalSymlinks(top) != filepath.EvalSymlinks(abs) → E2.
 3. gd := repoGit(ctx, abs, nil, nil, "rev-parse", "--absolute-git-dir").
    g := []string{"--git-dir=" + gd, "--work-tree=" + abs}   // every later repoGit call starts with g
 4. branch := "adlc/" + st.WorkflowID; ref := "refs/heads/" + branch
    repoGit(… g…, "check-ref-format", "--branch", branch) fails → E3.
 5. tip := repoGit(… g…, "for-each-ref", "--format=%(objectname)", ref)   // "" = no such branch
    tip != "": msg := repoGit(… g…, "log", "-1", "--format=%B", tip);
      msg has a line exactly "Adlc-Workflow: <workflow id>" →
        return tip, "branch " + branch + " in " + abs + " (already exported)", nil
        (a resume after a crash between update-ref and task_finished)
      else → E4.
 6. f := e.newFlow(w, st.WorkflowID)
    msg := f.git.run(ctx, "log", "-1", "--format=%B", st.BaseCommit); source := the value of its
    line "source-commit: <hash>" (no such line → E6: the workflow did not start from a repository).
    repoGit(… g…, "cat-file", "-e", source + "^{commit}") fails → E5.
 7. final := lastCommit(events); final == st.BaseCommit → E6.
    patch := f.git.run(ctx, "diff", "--binary", "--full-index", "--no-color", "--no-ext-diff",
                       "--no-textconv", "--no-renames", st.BaseCommit, final)      // raw bytes, not trimmed
    len(patch) == 0 → E6.
 8. idx := filepath.Join(<BaseDir>/<id>, "export.index"); os.Remove(idx) (ignore ErrNotExist); defer os.Remove(idx).
    env := []string{"GIT_INDEX_FILE=" + idx}
    repoGit(… env, g…, "read-tree", source)
    repoGit(… env, stdin = patch, g…, "apply", "--cached", "--whitespace=nowarn", "-")  fails → E7
    tree := repoGit(… env, g…, "write-tree")
 9. env2 := []string{"GIT_AUTHOR_DATE=<unix> +0000", "GIT_COMMITTER_DATE=<unix> +0000"}   (<unix> = e.o.Now().Unix())
    commit := repoGit(… env2, stdin = message, "-c", "user.name=adlc", "-c", "user.email=adlc@localhost",
                      "-c", "commit.gpgsign=false", g…, "commit-tree", tree, "-p", source, "-F", "-")
10. repoGit(… g…, "update-ref", "-m", "adlc: export workflow " + st.WorkflowID, ref, commit, "")
    The empty old value makes git refuse when the ref exists; on failure: the branch now exists
    (step 5 again) → E4, else E8.
11. detail := "branch " + branch + " in " + abs; when repoGit(… g…, "rev-parse", "--verify", "--quiet",
    "HEAD^{commit}") succeeds and differs from source, append " (HEAD has moved since the workflow started)".
    return commit, detail, nil
```

`-c` options go before `--git-dir` (they are git's global options; all of
`g`, `-c` and the subcommand's arguments end up after `git -c core.hooksPath=… -c core.fsmonitor=false`).

Error texts (`detail` of the failed `task_finished`):

| | Error |
|-|-------|
| E1 | `export: workflow has no input "repo"` |
| E2 | `export: <abs> is not the top level of a git repository` |
| E3 | `export: "<branch>" is not a valid branch name` |
| E4 | `export: branch <branch> already exists in <abs>` |
| E5 | `export: commit <source> that the workflow started from is not in <abs>` |
| E6 | `export: nothing to export: no task before "<t.ID>" changed the workspace` |
| E7 | `export: the workflow's change does not apply to <source>: <first line of git's stderr>` (should never happen: the base tree is the source tree) |
| E8 | `export: <the git error>` |

**Commit message** (ends with one `"\n"`; `<passed>` as in the gate summary;
the `Adlc-Approved-By` line only when `t.Gate == GateApproval`, with the
`resolved_by` of the last approving `gate_resolved` of `t.ID` in `events`):

```
adlc: workflow <workflow id>

Changes made by workflow <name>@<version>, tasks: <passed>.

Adlc-Workflow: <workflow id>
Adlc-Workflow-Digest: <st.Digest>
Adlc-Approved-By: <by>
```

Identity `adlc <adlc@localhost>` like W.3's internal commits: the commit
records what the tool produced; the person's own identity appears when they
merge, and their git config is not read for it.

**`repoGit`** — the only way this file runs git on the person's repository:

```go
// repoGit runs git in dir on the person's repository and returns its
// trimmed stdout. Program-running configuration is switched off for every
// call: no hook (also reference-transaction, which update-ref runs) and no
// fsmonitor. Inherited GIT_* variables are dropped so that only extraEnv
// applies. stdin may be nil.
func repoGit(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) (string, error)
```

- argv: `git -c core.hooksPath=/dev/null -c core.fsmonitor=false <args...>`; `cmd.Dir = dir`.
- env: `os.Environ()` without entries that start with `GIT_`, plus `GIT_TERMINAL_PROMPT=0`, plus `extraEnv`.
- error: `fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr))`.
- Never `push`, `fetch`, `remote`, `checkout`, `commit` (the porcelain command), `merge` or `reset`.

What this guarantees: no network; the person's working tree, index
(`.git/index`), HEAD and current branch are untouched (only the private index
and the object store are written, plus the one new ref); no hook or fsmonitor
program runs. Pushing and opening a pull request stay with the person.

### Validation rule (W.1 left it to W.5)

In W.1's `Validate`, after rule 25: for every task of kind export, the
workflow must have an input named `ExportInput` of kind `InputDir`; otherwise
`p.add(fmt.Sprintf("tasks[%d].kind", i), "export needs an input %q of kind dir", ExportInput)`.

### CLI (`internal/cli/workflow.go`)

```
adlc workflow run FILE --input NAME=VALUE ... [--id ID] [--db PATH] [--workflow-db PATH]
                       [--control-db PATH] [--fake-script AGENT_NAME=PATH ...] [--model-name NAME]
adlc workflow resume WORKFLOW_ID FILE [--db] [--workflow-db] [--control-db] [--fake-script ...] [--model-name]
adlc workflow approve WORKFLOW_ID TASK_ID FILE [--reject] [--comment TEXT] [--by NAME]
                       [--db] [--workflow-db] [--control-db] [--fake-script ...] [--model-name]
adlc workflow status [WORKFLOW_ID] [--workflow-db PATH]
```

`--fake-script` and `--model-name` are additions to m3.md's list, needed so
the same flags as `adlc run` work: a fake agent needs a script, and each agent
of a workflow may be a different fake. They mean what they mean for `adlc run`.

```go
// defaultWorkflowDBPath is the default --workflow-db (m3.md decision 2).
const defaultWorkflowDBPath = ".adlc/workflows.db"

// workflowOptions are the flags of the adlc workflow subcommands.
type workflowOptions struct {
	db, workflowDB, controlDB, modelName, id string
	inputs, fakeScripts                      []string // NAME=VALUE, AGENT_NAME=PATH
	reject                                   bool
	comment, by                              string
}

// openWorkflowSandbox opens the sandbox backend; tests pass a sandboxtest.Host.
type openWorkflowSandbox func(ctx context.Context) (sandbox.Sandbox, func(), error)

func newWorkflowCmd() *cobra.Command // Use "workflow", Short "Run, resume, approve and inspect workflows"; AddCommand the four below
func newWorkflowRunCmd() *cobra.Command     // "run FILE --input NAME=VALUE ...", cobra.ExactArgs(1)
func newWorkflowResumeCmd() *cobra.Command  // "resume WORKFLOW_ID FILE", cobra.ExactArgs(2)
func newWorkflowApproveCmd() *cobra.Command // "approve WORKFLOW_ID TASK_ID FILE", cobra.ExactArgs(3)
func newWorkflowStatusCmd() *cobra.Command  // "status [WORKFLOW_ID]", cobra.MaximumNArgs(1)

// the testable bodies; the cobra commands pass openDockerWorkflowSandbox
func workflowRun(ctx context.Context, o workflowOptions, file string, stdout io.Writer, open openWorkflowSandbox) error
func workflowResume(ctx context.Context, o workflowOptions, id, file string, stdout io.Writer, open openWorkflowSandbox) error
func workflowApprove(ctx context.Context, o workflowOptions, id, taskID, file string, stdout io.Writer, open openWorkflowSandbox) error
func workflowStatus(ctx context.Context, workflowDB, id string, stdout io.Writer) error
```

Flags are defined on each subcommand (not persistent), so the shared-default
tests can look them up: `--db` default `defaultDBPath`, `--workflow-db`
default `defaultWorkflowDBPath`, `--control-db` default `defaultControlDBPath`
(run, resume, approve); `status` has only `--workflow-db`. `--input` and
`--fake-script` are `StringArrayVar` (repeatable; values may contain commas).
`run`, `resume` and `approve` use `signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)` like `adlc run`.
`root.go`: add `root.AddCommand(newWorkflowCmd())` after `newForkCmd()`.

`openDockerWorkflowSandbox`: `docker.New(ctx)`; error →
`fmt.Errorf("workflow: Docker is not available: %w", err)`; close = `b.Close()`.

**Inputs** — `parseInputs(values []string, declared []workflow.Input) (map[string]string, error)`,
checked in this order, first problem wins:

| Problem | Error |
|---------|-------|
| no `=`, or empty name | `workflow: --input %q: want NAME=VALUE` |
| same name twice | `workflow: --input %s given twice` |
| name not declared | `workflow: unknown input %q (the workflow declares: <names sorted, ", ">)` |
| declared, not given (first in file order) | `workflow: missing --input %s (kind %s)` |
| kind dir, not a directory | `workflow: input %s: %s is not a directory` |
| kind file, not a regular file | `workflow: input %s: %s is not a file` |

Values of kind `dir` and `file` are replaced by `filepath.Abs(value)` (a resume
from another directory must find them; `workflow_started.inputs` records the
absolute path). `text` values are kept as given.

When the workflow has a task of kind export, `workflowRun` also checks that
`inputs["repo"]` is the top level of a git repository (`git -c core.hooksPath=/dev/null -C <dir> rev-parse --show-toplevel`,
compared after `filepath.EvalSymlinks`); otherwise
`workflow: input repo: <dir> is not the top level of a git repository`. This
fails before any model call instead of at the end of the workflow.

**`--id`**: default `w.ID(time.Now())`. A given id must match
`^[A-Za-z0-9][A-Za-z0-9._@-]{0,127}$`, contain neither `..` nor `@{`, and not
end with `.` or `.lock` (it becomes a directory name and part of a git branch
name); else `workflow: invalid --id %q`.

**`--fake-script`** — `parseFakeScripts(values []string) (map[string]string, error)`:
`AGENT_NAME=PATH`; no `=` or an empty side → `workflow: --fake-script %q: want AGENT_NAME=PATH`;
the same name twice → `workflow: --fake-script for agent %q given twice`.
The key is the agent's `name:` (not its file name and not `name@version`).

**Agent references** — `workflowAgentPath(w *workflow.Workflow, ref string) string`:
`controlplane.ParseRef(ref)` succeeds → `ref` (a registry reference, resolved by
`resolveAgentRef`); `filepath.IsAbs(ref)` → `filepath.Clean(ref)`; else
`filepath.Join(w.Dir, ref)`. This is the same rule as W.1's `agentFile`.

**Wiring** — one value carries what the engine's callbacks need:

```go
// workflowWiring builds child runs for the engine (RunTask, AgentBudget).
type workflowWiring struct {
	wf          *workflow.Workflow
	runs        *eventlog.Store    // the run log, .adlc/events.db
	killSwitch  harness.KillSwitch // nil = none
	controlDB   string             // for NAME@N / NAME@ENV
	modelName   string             // --model-name
	fakeScripts map[string]string  // agent name -> fake script
	startHook   hookStarter        // nil = startPluginHook
}

// loadAgent resolves ref (relative to the workflow file, or in the registry)
// and loads it; cleanup removes a materialized registry version.
func (ww workflowWiring) loadAgent(ctx context.Context, ref string) (*agentconfig.Agent, func(), error)
// checkAgents loads every agent and critic of the workflow once, before the
// engine starts, so a bad agent file fails before any model call.
func (ww workflowWiring) checkAgents(ctx context.Context) error
// runTask is Options.RunTask.
func (ww workflowWiring) runTask(ctx context.Context, tr workflow.TaskRun) (*eventlog.RunState, error)
// agentBudget is Options.AgentBudget.
func (ww workflowWiring) agentBudget(ref string) (*adlcv1.Budget, error)
```

`loadAgent(ctx, ref)`: `ro := runOptions{agentPath: workflowAgentPath(ww.wf, ref), controlDB: ww.controlDB, modelName: ww.modelName}`;
`ro, cleanup, err := resolveAgentRef(ctx, ro)`; `a, err := loadAgent(ro)` (call cleanup on error);
then refuse, wrapping with the reference:

| Problem | Error |
|---------|-------|
| `len(a.Tools) > 0` | `workflow: agent %s declares tools; a workflow task gets the sandbox tools only` |
| `a.Sandbox != nil` | `workflow: agent %s declares a sandbox; the workflow provides the sandbox` |

`checkAgents(ctx)`: for every task of kind agent in file order, `loadAgent`
its `Agent`, and its `Review.Critic` when set; and for each loaded agent with
`a.Model.Provider == "fake"` (or a fake fallback or plan role), `ww.fakeScripts[a.Name] == ""` →
`workflow: task %s: agent %s uses provider fake; give --fake-script %s=PATH` (task id, ref, `a.Name`).
Call it in run, resume and approve before `workflow.New`.

`runTask(ctx, tr)` — `evalRunAgent`'s shape, plus hooks, the kill switch and the ids:

1. `a, cleanup, err := ww.loadAgent(ctx, tr.AgentRef)`; `defer cleanup()`.
2. `script := ww.fakeScripts[a.Name]` (only used when a provider is fake; `buildAdapter` reports a missing one).
3. `m := buildModel(a, script)`; `pm := buildPlanModel(a, script)`.
4. `tools := sandboxset.NewRouter(ctx, sandboxtool.Options{Sandbox: tr.Sandbox, Handle: tr.Handle})`
   (error → `fmt.Errorf("workflow: start sandbox tools: %w", err)`); `defer tools.Close()`.
5. `hooks, closeHooks := startHooks(ctx, a, ww.startHook)`; `defer closeHooks()`.
6. `cfg := harnessConfig(a, tr.RunID, tr.Prompt)`; `cfg.Budget = tr.Budget` (already nested by the engine; nil = no limit);
   `cfg.WorkflowID = tr.WorkflowID`; `cfg.TaskID = tr.Task.ID`; `cfg.Hooks = hooks`.
7. `return harness.Run(ctx, cfg, harness.Deps{Log: ww.runs, Model: m, PlanModel: pm, Tools: tools,
   Policy: a.PolicyEngine(), KillSwitch: ww.killSwitch})`.

`agentBudget(ref)`: `a, cleanup, err := ww.loadAgent(context.Background(), ref)`; `defer cleanup()`; return `a.BudgetProto(), nil`.

**Opening everything** — `openWorkflowEngine(ctx, o, w, stdout, open, existing bool) (*workflow.Engine, *workflowEnv, error)`
(`workflowEnv` holds the stores and a `close()` that closes everything opened, in reverse order):

1. `existing` (resume, approve): `--workflow-db` must exist, else `workflow log <path> does not exist`.
   Otherwise (`run`): `os.MkdirAll(filepath.Dir(o.workflowDB), 0o755)` and `os.MkdirAll(filepath.Dir(o.db), 0o755)`.
2. `wlog := eventlog.Open(ctx, o.workflowDB, eventlog.WithFirstKind("workflow_started"))`.
3. `runs := eventlog.Open(ctx, o.db)` (created when missing, also for resume: a workflow parked before its first child run has none).
4. `ks, closeKS := openKillSwitch(ctx, o.controlDB)` (missing file = no kill switch).
5. `scripts := parseFakeScripts(o.fakeScripts)`; `ww := workflowWiring{...}`; `ww.checkAgents(ctx)`.
6. `sb, closeSB := open(ctx)`.
7. `workflow.New(workflow.Options{Log: wlog, Runs: runs, Sandbox: sb, BaseDir: workflowBaseDir(o.workflowDB),
   RunTask: ww.runTask, AgentBudget: ww.agentBudget, Now: time.Now, Stdout: stdout, Gates: workflow.GatePark})`.

`workflowBaseDir(db) = filepath.Join(filepath.Dir(db), "workflows")`: with the
default `.adlc/workflows.db` this is `.adlc/workflows` (m3.md decision 4); a
test with `--workflow-db <tmp>/w.db` keeps its workspaces in `<tmp>/workflows`.

**`workflowRun`**: `w := workflow.Load(file)`; `parseInputs`; the export
repo check; the id; `openWorkflowEngine(…, existing=false)`;
`st, err := eng.Start(ctx, w, id, inputs)`; error → exit 1; print the run
summary; exit code below. Every error before `Start` leaves no file behind
(the input checks run before any store is opened).

**`workflowResume`**: `w := workflow.Load(file)`; open (existing); `eng.Resume(ctx, w, id)`; error → exit 1
(`ErrWorkflowNotFound`, `ErrWorkflowChanged`, `ErrWorkflowFinished` keep their text); summary; exit code.

**`workflowApprove`**: `by := approverName(o.by, os.Getenv)`; `w := workflow.Load(file)`; open (existing);
`st, err := eng.ResolveGate(ctx, w, id, taskID, !o.reject, by, o.comment)`; error → exit 1;
print `gate of task <task> approved by <by>` or `gate of task <task> rejected by <by>`, then the summary; exit code.

**Exit codes** (m3.md): `st.Status == "completed"` and outcome `COMPLETED` → 0;
`"awaiting_approval"` → 0 (parked; the `next:` line says what to do);
`"completed"` with any other outcome → `&exitError{code: 2}` (a rejected gate is 2);
`"running"` (the engine returned without finishing, e.g. interrupted) → exit 1 with
`workflow <id> stopped before it finished; continue it with: adlc workflow resume <id> <file>`.
Usage and infrastructure errors → 1.

### Output (`internal/cli/workflow_print.go`)

**Run summary** — `printWorkflowSummary(w io.Writer, s workflowSummary)` after
run, resume and approve. Every line is `fmt.Fprintf(w, "%-11s%s\n", label, value)`.
Lines, in this order (omitted lines are noted):

| Label | Value |
|-------|-------|
| `workflow:` | workflow id |
| `status:` | `st.Status` |
| `outcome:` | only when status is `completed`: outcome name, lower case, without `WORKFLOW_OUTCOME_` |
| `detail:` | only when `st.Detail != ""` |
| `tasks:` | `<n passed>/<len(w.Tasks)> passed` (`TaskState.Passed`, tasks of the file) |
| `spent:` | `$<formatUSD(usd)>  tokens <n>  runs <k>` (from `workflowSpend`, below) |
| `workspace:` | `eng.Workspace(id)` |
| `branch:` | one line per export task with `Passed`: `adlc/<id> in <st.Inputs["repo"]>` |
| `next:` | one line per task in `w.Order()` with `GatePending`: `adlc workflow approve <id> <task> <file> [--reject]` |
| `next:` | one line per task whose `Outcome` is `TASK_OUTCOME_AWAITING_APPROVAL` while status is `awaiting_approval`: `adlc approve <approval id> <LastRunID> AGENT_YAML, then adlc workflow resume <id> <file>` (`<approval id>` = the first of `runs.State(LastRunID).PendingApprovals`; `AGENT_YAML` literally: the task's agent or critic file) |

`<file>` is FILE as given on the command line.

`workflowSpend(ctx, runs *eventlog.Store, st *workflow.State) (*eventlog.RunState, error)`
sums the child runs: for every `TaskState` and every id in `RunIDs`,
`runs.State(ctx, id)`; `eventlog.ErrRunNotFound` → skip (the run never wrote
an event); other errors → return them. The result has `RunID` = workflow id,
`AgentID` = `<name>@<version>`, `StartedAt`/`LastEventAt` from `st`, and the
sums of `Step`, `ModelCalls`, `ToolCalls`, `ToolErrors`, `Denials`, `Errors`
and every `Usage` field. Return also the number of runs found (`(… , n int, err)`).
Tokens printed = input + output + cache read + cache write. (W.6 reuses this.)

Example after `run` of the CLI test fixture (parked):

```
workflow:  flow@1-20261114T100000Z-0a1b2c
status:    awaiting_approval
tasks:     1/2 passed
spent:     $0.000000  tokens 140  runs 1
workspace: <tmp>/workflows/flow@1-20261114T100000Z-0a1b2c/workspace
next:      adlc workflow approve flow@1-20261114T100000Z-0a1b2c release <tmp>/flow.yaml [--reject]
```

The engine's own progress lines (W.3, `Options.Stdout`) come before the summary.

**`adlc workflow status`** (no id) — `openExistingWorkflowLog(ctx, path)`
(missing file → `workflow log <path> does not exist`, exit 1; opened with
`WithFirstKind("workflow_started")`); `ids := store.Runs(ctx)`; fold each
(error → exit 1, `workflow status: workflow %q: %w`). No workflow → exactly
`no workflows\n`. Else a `tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)` table, every
line written with `"%s\t%s\t%s\t%s\t%s\t%s\n"` (no trailing tab), rows in id order:

```
WORKFLOW                        STATUS             OUTCOME  PASSED  STARTED               LAST
flow@1-20261114T100000Z-0a1b2c  awaiting_approval  -        1       2026-11-14T10:00:00Z  2026-11-14T10:00:05Z
```

OUTCOME = lower-case name without prefix, `-` while unspecified. PASSED = number of
`TaskState`s with `Passed`. Times = `UTC().Format(time.RFC3339)`.

**`adlc workflow status WORKFLOW_ID`** — unknown id → exit 1 with
`fmt.Errorf("workflow status: %w: %q", workflow.ErrWorkflowNotFound, id)`. Otherwise
`%-11s` label lines, then a blank line and a table:

```
workflow:  <id>
name:      <name>@<version>
digest:    <digest>
status:    <status>
outcome:   <outcome>            (only when completed)
detail:    <detail>             (only when not empty)
started:   <StartedAt RFC3339 UTC>
last:      <LastEventAt RFC3339 UTC>
inputs:    <name=value pairs sorted by name, joined with one space>

TASK       ATTEMPTS  ROUNDS  OUTCOME  DONE  GATE     COMMIT        RUNS
write      1         0       passed   yes   -        1a2b3c4d5e6f  run-…
release    0         0       -        no    pending  -             -
```

Rows: the ids of `st.TaskOrder`, then the other keys of `st.Tasks` sorted.
OUTCOME = `running` when `Running`, else the task outcome lower case without
`TASK_OUTCOME_`, `-` while unspecified. DONE = `yes`/`no` (`Passed`).
GATE = `pending` (`GatePending`), `approved` (`GateApproved`), else `-`.
COMMIT = first 12 characters, `-` when empty. RUNS = `RunIDs` joined with `,`,
`-` when none. No task state at all → the line `no task has started` instead
of the table. Same tabwriter settings and `%s\t…\n` format as the list.

### Tool approvals inside a workflow (`internal/cli/approve.go`)

m3.md decision 10: a child run that parks for a tool approval parks the
workflow; after `adlc approve` finishes that run, `adlc workflow resume` picks
it up. W.3 already does its half: the parked run's sandbox is **kept**
(labelled `adlc.run_id` = the child run id, `task_finished{AWAITING_APPROVAL, detail "sandbox <id>"}`),
and `Resume` collects the run once it has finished (`execContinue`) and
destroys the sandbox. Today's `adlc approve` cannot continue such a run: it
builds the tools from the agent file (`buildRun`), but a workflow child's
tools are the in-process sandbox tool router on that kept sandbox, which the
agent file does not name. This task adds that path.

```go
// taskSandbox is the sandbox backend for continuing a workflow child run;
// *docker.Backend implements it.
type taskSandbox interface {
	sandbox.Sandbox
	FindByRunID(ctx context.Context, runID string) ([]sandbox.Handle, error)
	Close() error
}
```

`approveOptions` gets one unexported field `openTaskSandbox func(context.Context) (taskSandbox, error)`
(nil = `openDockerTaskSandbox`, which wraps `docker.New` like `openSandboxBackend`); tests set it.
`approveRun`'s signature does not change.

In `continueRun`, after `loadAgent` and `openKillSwitch`, and **before**
`attachSandbox`: when the run's state (from `CheckApproval`; pass it in) has
`WorkflowID != ""`, call `continueWorkflowRun` instead of the rest:

```go
// continueWorkflowRun continues a paused child run of a workflow on the
// sandbox the engine kept for it, with the same tools, budget and ids.
func continueWorkflowRun(ctx context.Context, o approveOptions, a *agentconfig.Agent, store *eventlog.Store,
	st *eventlog.RunState, res harness.Resolution, ks harness.KillSwitch) (*eventlog.RunState, error)
```

1. Refuse `a.Tools` / `a.Sandbox` with W.5's two `loadAgent` errors (workflow agents never have them).
2. `b, err := open(ctx)`; `defer b.Close()`; `h, err := findLiveSandbox(ctx, b, st.RunID)` (resume.go;
   `b` satisfies `sandboxLookup`). Its "not running" message is the right one here too.
3. `tools := sandboxset.NewRouter(ctx, sandboxtool.Options{Sandbox: b, Handle: h})`; `defer tools.Close()`.
4. `hooks, closeHooks := startHooks(ctx, a, o.startHook)`; models from `buildModel(a, o.fakeScript)`, `buildPlanModel`.
5. `cfg := harnessConfig(a, st.RunID, "")`; `cfg.Budget = st.Budget` (the nested budget the run was started
   with, not the agent's own); `cfg.WorkflowID, cfg.TaskID = st.WorkflowID, st.TaskID`; `cfg.Hooks = hooks`.
6. `return harness.Approve(ctx, cfg, harness.Deps{Log: store, Model, PlanModel, Tools: tools, Policy: a.PolicyEngine(), KillSwitch: ks}, res)`.
   The sandbox is **not** destroyed: `adlc workflow resume` copies its workspace out and destroys it.

After `printSummary`, `approveRun` prints for such a run one more line:
`workflow: run <run id> is task <task id> of workflow <workflow id>; continue it with: adlc workflow resume <workflow id> FILE`.
The exit code rule is unchanged (0 completed, 2 otherwise).
`ResolveApproval` (more than one pending approval) needs no change.

### `interfaces.md`

§6, in the `Config` block after `Plan`:

```go
	WorkflowID string // task W.5: RunStarted.workflow_id; "" outside a workflow
	TaskID     string // task W.5: RunStarted.task_id
```

In the M3 section (`## §12 M3 additions (briefs W.1–W.6)`, created by W.1–W.3;
if it does not exist, add it with that heading after §11), add:

```
### Gates and export (W.5)

type GatePolicy int; const GatePark, GateAutoApprove; Options.Gates GatePolicy
var ErrNoPendingGate = errors.New("workflow: no pending gate for this task")
func (e *Engine) ResolveGate(ctx, w, id, taskID string, approved bool, by, comment string) (*State, error)
const ExportInput = "repo"
```

and this paragraph: "A gated task parks the workflow before it starts
(`gate_requested`, summary of four lines); `GateAutoApprove` adds
`gate_resolved{approved, by "auto"}`. A rejection ends the workflow
`REJECTED`. An export task applies the diff between the workflow's base and
its last commit to the source commit of input `repo` (the HEAD the workflow
started from, `source-commit:` in the base commit) in a private index and
creates `refs/heads/adlc/<workflow id>` with parent = that commit, via
`update-ref` (refused if it exists); hooks and fsmonitor are off, nothing is
pushed, the working tree, index, HEAD and current branch are not touched.
`adlc approve` continues a parked workflow child run on its kept sandbox. CLI: `adlc workflow run|resume|approve|status`,
`defaultWorkflowDBPath = ".adlc/workflows.db"`, workspaces under
`<dir of --workflow-db>/workflows`; child runs get the sandbox tools, the kill
switch, hooks, the nested budget and `Config.WorkflowID`/`TaskID`."

## Steps

1. Branch from the latest `main` (AGENTS.md). Read W.3's `engine.go`: find the hook block and the helpers listed under "Where the two hooks go", and check that a parked child run keeps its sandbox (`task_finished{AWAITING_APPROVAL, "sandbox <id>"}`) and that the base commit has the `source-commit:` line. If any of this is missing or different, stop and report.
2. Harness: `Config.WorkflowID`, `Config.TaskID`, the `run_started` change; `workflow_ids_test.go`. `go test ./internal/harness/` passes.
3. `gate.go`: `GatePolicy` and its constants, `ErrNoPendingGate`, `checkGate`, `gateSummary`, `ResolveGate`; `Options.Gates`; delete the `checkGate` stub from `engine.go`.
4. `gate_test.go`; see each test fail once by breaking the code (for example skip step 2 of `checkGate`).
5. `export.go`: `ExportInput`, `repoGit`, `export`, `runExport`; delete the `runExport` stub from `engine.go`; `repoInput = ExportInput`.
6. `export_test.go`; break `-c core.hooksPath=/dev/null` once and see `TestExportRunsNoHooks` fail.
7. The validation rule and its row in W.1's validate test.
8. `internal/cli/workflow.go`, `workflow_print.go`, the testdata files, `root.go`; then `workflow_test.go`, `cli_test.go`.
9. `approve.go`: `taskSandbox`, `continueWorkflowRun`, the extra line; its tests.
10. `interfaces.md`.
11. `make fmt && make ci`.

## Tests

### `internal/harness/workflow_ids_test.go`

| Test | Asserts |
|------|---------|
| `TestRunRecordsWorkflowAndTask` | `Run` with `cfg.WorkflowID = "wf@1-x"`, `cfg.TaskID = "spec"` and the fake `[textTurn("done")]` → the first event's `run_started` has `workflow_id == "wf@1-x"`, `task_id == "spec"`; the folded state has `WorkflowID`/`TaskID` set |
| `TestRunWithoutWorkflowLeavesIDsEmpty` | default config → both fields `""` |

### `internal/workflow/gate_test.go` and `export_test.go`

Build on W.3's `helpers_test.go` (`newTestEnv`, `clock`, `fakeRunner`/`fakeStep`,
`makeRepo`, `parseWorkflow`, `kinds`, `recordingSandbox`); add only what is missing:

- `newGateEnv(t, gates GatePolicy) *testEnv`: `newTestEnv(t)`, then a new engine on the same
  stores and sandbox with `Gates: gates` (add a small options hook to `newTestEnv` if that is simpler).
  Its clock starts at `2026-11-14T10:00:00Z`.
- `repo := makeRepo(t, map[string]string{"README.md": "readme\n"})`.
- The workflow (`parseWorkflow`; W.3's `parseWorkflow` writes no agent file, and the fake never loads one):

```yaml
name: flow
version: 1
inputs:
  - {name: repo, kind: dir}
tasks:
  - id: write
    agent: a.yaml
    prompt: write
  - id: release
    kind: export
    depends_on: [write]
    gate: approval
```

- `fakeRunner` steps for `write`: `fakeStep{sh: "printf 'hi\n' > out.txt", result: "done", usd: 1000, tokens: 150}`
  (W.3's fake puts `tokens` into input tokens: 150 tokens, $0.001000).
- `flowID = "flow@1-20261114T100000Z-0a1b2c"`; `te.eng.Start(ctx, w, flowID, map[string]string{"repo": repo})`.

| Test | Asserts |
|------|---------|
| `TestGateParksBeforeTask` | after `Start`: status `awaiting_approval`; the last event is `gate_requested{task_id "release"}`; no `task_started` for `release`; `Tasks["release"].GatePending`; no branch `adlc/<id>` in the repo |
| `TestGateSummaryText` | the `gate_requested.summary` equals exactly the four-line example above |
| `TestGateDoesNotRunLaterTasks` | a workflow `a` → `b (gate)` → `c`, all agent tasks: after `Start` only `a` has `task_started`; the fake was called once |
| `TestResumeParkedAtGateAppendsNothing` | `Resume` on the parked workflow → same `LastSeq`, status `awaiting_approval`, `RunTask` not called again |
| `TestResolveGateApproveContinues` | `ResolveGate(…, "release", true, "bob", "ok")` → appended kinds start `gate_resolved, task_started, task_finished, workflow_completed`; `gate_resolved{approved, "bob", "ok"}` with `parent_seq` = seq of `gate_requested`; status `completed`, outcome `COMPLETED`; `Tasks["release"].Passed` |
| `TestResolveGateRejectEndsRejected` | `approved=false, by "bob", comment "no"` → exactly `gate_resolved`, `workflow_completed{REJECTED, "gate of task release rejected by bob: no"}` appended; no `task_started` for `release`; no branch |
| `TestResolveGateRejectWithoutComment` | detail `gate of task release rejected by bob` |
| `TestResolveGateNoPendingGate` | task `write` → `ErrNoPendingGate`; task `nope` → `ErrNoPendingGate`; approve twice → the second is `ErrWorkflowFinished` (the workflow completed); log length unchanged by the refused calls |
| `TestResolveGateUnknownWorkflow` | id `flow@1-none` → `errors.Is(err, ErrWorkflowNotFound)` |
| `TestResolveGateWorkflowChanged` | `w.Digest = "sha256:other"` → `ErrWorkflowChanged`; nothing appended |
| `TestResolveGateNeedsBy` | `by == ""` → error contains `resolved by is empty`; nothing appended |
| `TestGateAutoApprove` | `GateAutoApprove`: one `Start` completes the workflow; kinds contain `gate_requested` immediately followed by `gate_resolved{approved, "auto", ""}`; branch exists |
| `TestExportCreatesBranch` | after approve: `git rev-parse adlc/<id>^` = the repo's HEAD at Start (the source commit); `git show adlc/<id>:out.txt` = `hi\n`; `git show adlc/<id>:README.md` = `readme\n`; the repo's HEAD, current branch (`main`) and `git status --porcelain` (empty) are unchanged; `task_finished{role EXPORT, outcome PASSED, detail "branch adlc/<id> in <repo>", commit = the branch tip}` |
| `TestExportCommitMessage` | `git log -1 --format=%B adlc/<id>` equals the message above with `tasks: write`, digest `w.Digest`, `Adlc-Approved-By: bob`; author and committer `adlc <adlc@localhost>`, author date `2026-11-14T10:00:00Z` |
| `TestExportAfterHeadMoved` | after `Start`, commit on the repo's `main` a new `other.txt` and `README.md` = `theirs\n`; approve → `PASSED`; the branch's parent is the source commit (not the new HEAD); the branch has `out.txt`, `README.md` = `readme\n` and no `other.txt`; detail ends with ` (HEAD has moved since the workflow started)`; `main` still points at the new commit |
| `TestExportKeepsUntouchedSymlink` | the source commit has a symlink `link -> README.md` (W.3's workspace skips it); after export, `git ls-tree adlc/<id> link` shows mode `120000` (the diff never deleted it) |
| `TestExportSourceCommitGone` | after `Start`, `git -C repo` rewrites history so the source commit is unreachable and pruned (`git reset --hard` to a new root commit, `git reflog expire --expire=now --all`, `git gc --prune=now`) → detail starts with `export: commit` and ends with `that the workflow started from is not in <repo>`; no branch |
| `TestExportRefusesExistingBranch` | create branch `adlc/<id>` at HEAD before approving → detail `export: branch adlc/<id> already exists in <repo>`; workflow `FAILED`; the branch still points at the old HEAD |
| `TestExportIsIdempotent` | approve once (branch created); fold the log and call the inner `e.export(ctx, w, st, releaseTask)` again (what a resume after a crash between `update-ref` and `task_finished` does) → no error, the same commit, detail `branch adlc/<id> in <repo> (already exported)`; the branch tip is unchanged |
| `TestExportRunsNoHooks` | before approving, install executable `reference-transaction`, `pre-commit`, `post-commit` hooks in `<repo>/.git/hooks` and in a dir set with `git config core.hooksPath <dir>`, each `touch <tmp>/ran-<name>`; after the export no `ran-*` file exists and the branch exists |
| `TestExportNeverPushes` | the repo has `origin` = a bare repo (`git init --bare`); after the export the bare repo has no ref under `refs/heads/adlc/` (`git --git-dir=<bare> for-each-ref`) |
| `TestExportNeedsTopLevel` | input `repo` = a subdirectory of a git repo → detail `export: <abs> is not the top level of a git repository`; nothing created |
| `TestExportNothingToExport` | a workflow whose task changes nothing (`fakeStep{result: "done"}`, no `sh`) → detail `export: nothing to export: no task before "release" changed the workspace` |
| `TestValidateExportNeedsRepoInput` | (in W.1's validate test table) an export task in a workflow whose only input is `issue` (file) → `tasks[1].kind: export needs an input "repo" of kind dir` |

### `internal/cli/workflow_test.go`

Fixture `internal/cli/testdata/workflow/`:

`flow.yaml`:

```yaml
name: flow
version: 1
inputs:
  - name: repo
    kind: dir
tasks:
  - id: write
    agent: writer.yaml
    prompt: |
      Write out.txt with the text hi.
    checks:
      - test -f out.txt
  - id: release
    kind: export
    depends_on: [write]
    gate: approval
```

`writer.yaml`:

```yaml
name: writer
version: 1
model:
  provider: fake
system: You write files in /workspace with the files tools.
budget:
  max_steps: 5
  max_tool_calls: 5
  max_wall_clock: 2m
policy:
  default: deny
  rules:
    - {id: read,  risk: read,        decision: allow}
    - {id: write, risk: write_local, decision: allow}
```

`writer.fake.json`:

```json
{
  "turns": [
    {"text": "Writing out.txt.",
     "tool_calls": [{"id": "call_1", "name": "files__write_file", "arguments": {"path": "out.txt", "content": "hi\n"}}],
     "finish": "tool_use", "usage": {"input_tokens": 50, "output_tokens": 20}},
    {"text": "Wrote out.txt.", "finish": "end_turn", "usage": {"input_tokens": 60, "output_tokens": 10}}
  ]
}
```

Helpers: `hostSandbox(t) openWorkflowSandbox` returns `sandboxtest.NewHost(t.TempDir())` and a no-op close;
`flowEnv(t)` copies the three fixture files into `t.TempDir()` (so workspaces and dbs stay in the temp dir),
makes a git repo (`git init -q -b main`, `README.md` = `readme\n`, one commit with `-c user.name=t -c user.email=t@localhost -c commit.gpgsign=false`; skip without `git`), and returns options with `db`, `workflowDB`, `controlDB` in the
temp dir, `inputs: ["repo=<repo>"]`, `fakeScripts: ["writer=<tmp>/writer.fake.json"]`, `id: flowID`;
`codeOf(err) int` = 0 for nil, `exitError.code`, else 1.

| Test | Asserts |
|------|---------|
| `TestWorkflowRunParksAtGate` | `workflowRun` → code 0; stdout ends with exactly the six summary lines of the example (with the temp paths); no branch `adlc/<id>` |
| `TestWorkflowChildRunRecordsWorkflowAndTask` | after run: the only run in `--db` has `WorkflowID == flowID`, `TaskID == "write"`; its `run_started.budget` equals `writer.yaml`'s budget (no workflow budget, so nesting keeps it) |
| `TestWorkflowChildBudgetIsNested` | `flow.yaml` with `budget: {max_wall_clock: 1m}` → the child's `run_started.budget.max_wall_clock` is 1m (not the agent's 2m) |
| `TestWorkflowApproveExportsBranch` | run, then `workflowApprove(…, flowID, "release", file)` with `by: "bob"` → code 0; stdout starts with `gate of task release approved by bob`; contains `status:    completed`, `outcome:   completed`, `tasks:     2/2 passed`, `branch:    adlc/<id> in <repo>`; `git -C <repo> show adlc/<id>:out.txt` = `hi\n` |
| `TestWorkflowApproveReject` | `reject: true, comment: "no", by: "bob"` → code 2; `outcome:   rejected`, `detail:    gate of task release rejected by bob: no`; no branch |
| `TestWorkflowApproveNoPendingGate` | task `write` → code 1, error contains `no pending gate` |
| `TestWorkflowResumeParked` | `workflowResume` on the parked workflow → code 0, status `awaiting_approval`, workflow log length unchanged |
| `TestWorkflowResumeFinished` | after approve → resume code 1, error contains `already completed` |
| `TestWorkflowResumeMissingLog` | `--workflow-db` that does not exist → code 1, `workflow log <path> does not exist` |
| `TestWorkflowRunInputErrors` | table, each code 1 with the exact message of the inputs table, and `--workflow-db` not created: `repo` (no `=`); `=x`; `repo=a` twice; `nope=x`; no `--input`; `repo=<a file>`; `repo=<a subdirectory of a git repo>` → `is not the top level of a git repository` |
| `TestWorkflowRunInvalidID` | `--id a/b`, `--id x..y`, `--id x.lock` → code 1, `invalid --id` |
| `TestWorkflowRunExistingID` | run twice with the same `--id` → second code 1, `already exists` |
| `TestWorkflowNeedsFakeScript` | no `--fake-script` → code 1, `workflow: task write: agent writer.yaml uses provider fake; give --fake-script writer=PATH` (the ref printed as written in the file); `--db` has no run |
| `TestWorkflowAgentWithToolsRefused` | `writer.yaml` with a `tools:` entry → code 1, `declares tools` |
| `TestWorkflowKillSwitchStopsChild` | control db with `Kill(ScopeAgent, "writer", "stop")` → the child run ends `CANCELLED` with detail `killed: stop`; workflow outcome `FAILED`; code 2 |
| `TestWorkflowRegistryRef` | register `writer.yaml` in the control db (`controlplane.Register`); `flow.yaml` with `agent: writer@1` → code 0 (parked), the child's `AgentID` is `writer@1` |
| `TestWorkflowStatusList` | after a parked run: `workflowStatus(ctx, wdb, "")` → two lines; `strings.Fields` of the first is `[WORKFLOW STATUS OUTCOME PASSED STARTED LAST]`, of the second `[<id> awaiting_approval - 1 <started> <last>]`; no line ends with a space |
| `TestWorkflowStatusDetail` | contains `status:    awaiting_approval`, `inputs:    repo=<repo>`, a `write` row with fields `[write 1 0 passed yes - <12 hex> <run id>]` and a `release` row `[release 0 0 - no pending - -]` |
| `TestWorkflowStatusEmpty` | empty workflow log (open and close it) → exactly `no workflows\n` |
| `TestWorkflowStatusUnknown` | code 1, `workflow not found` |
| `TestWorkflowStatusMissingDB` | code 1, `workflow log <path> does not exist` |
| `TestParseInputs` | table for every row of the inputs table plus a valid case (dir and file made absolute, text kept) |
| `TestParseFakeScripts` | `a=p` ok; `a`, `=p`, `a=` rejected; `a=p a=q` rejected |
| `TestWorkflowAgentPath` | `Dir "/wf"`: `writer.yaml` → `/wf/writer.yaml`; `/x/a.yaml` → `/x/a.yaml`; `coder@1`, `coder@prod` → unchanged |
| `TestApproveContinuesWorkflowChildRun` (`workflow_test.go`) | `flow.yaml` with `writer.yaml`'s `write` rule set to `decision: needs_approval`; one `recordingHost` (a `sandboxtest.Host` that remembers `Spec.RunID` → handle and implements `FindByRunID` and `Close`) serves both `workflowRun` and `approveOptions.openTaskSandbox`. `workflowRun` → code 0, status `awaiting_approval`, a `next:` line `adlc approve a-call_1 <child run id> AGENT_YAML, then adlc workflow resume …`; `approveRun(ctx, o, "a-call_1", <child>, …)` with a fake script holding only the turn `Wrote out.txt.` → code 0, `stop:    completed`, the extra `workflow:` line; the child's last events are `tool_call_completed`, `checkpoint`, …, `run_completed{COMPLETED}`; then `workflowResume` → the task passes (commit has `out.txt`), the workflow parks at the `release` gate; the recorded sandbox was destroyed |
| `TestApproveWorkflowChildSandboxGone` | as above, but destroy the kept sandbox before `approveRun` → code 1, stderr contains `is not running`; nothing appended to the child run |
| `TestApproveWorkflowChildUsesRunBudget` | as `TestApproveContinuesWorkflowChildRun`, with `flow.yaml` `budget: {max_tokens: 100}` (the child's nested `max_tokens` is 100; `writer.yaml` has none); the approve script is two turns: a `files__list_dir` call with usage 20/20, then `Done.` → the continued run stops `budget_exhausted` before the second model call (70 + 40 tokens ≥ 100); exit code 2. With the agent's own budget it would have completed |

`cli_test.go`: add `"workflow run": newWorkflowRunCmd()`, `"workflow resume": newWorkflowResumeCmd()`,
`"workflow approve": newWorkflowApproveCmd()` to `TestCommandsShareDefaultDB` and
`TestCommandsShareDefaultControlDB`; new `TestWorkflowCommandsShareDefaultWorkflowDB`:
`defaultWorkflowDBPath == ".adlc/workflows.db"` and the `--workflow-db` default of all four subcommands equals it.

## Verify

```bash
go test -race ./internal/harness/ -run 'WorkflowAndTask|WithoutWorkflow' -v      # PASS
go test ./internal/harness/                                                     # existing tests unchanged, PASS
go test -race ./internal/workflow/ -run 'Gate|Export|ValidateExport' -v          # all PASS
go test -race ./internal/cli/ -run 'Workflow|ParseInputs|ParseFakeScripts|ApproveRefusesWorkflow|CommandsShare' -v   # all PASS
make ci                                                                         # passes
```

By hand (needs Docker):

```bash
make build
repo=$(mktemp -d) && git -C "$repo" init -q -b main && echo readme > "$repo/README.md" \
  && git -C "$repo" add -A && git -C "$repo" -c user.name=me -c user.email=me@localhost commit -q -m base
./bin/adlc workflow run internal/cli/testdata/workflow/flow.yaml --input repo="$repo" \
  --fake-script writer=internal/cli/testdata/workflow/writer.fake.json --db bin/wf-events.db --workflow-db bin/wf.db
# status: awaiting_approval, next: adlc workflow approve <id> release ...
./bin/adlc workflow status --workflow-db bin/wf.db
./bin/adlc workflow approve <id> release internal/cli/testdata/workflow/flow.yaml --db bin/wf-events.db --workflow-db bin/wf.db \
  --fake-script writer=internal/cli/testdata/workflow/writer.fake.json
git -C "$repo" branch --list 'adlc/*'          # adlc/<id>
git -C "$repo" status --porcelain              # empty
```

## Out of scope

- Pushing the branch or opening a pull request (the person does it; design §3.6).
- Killing a whole workflow (`adlc kill run <child run id>` and `adlc kill agent` work on child runs).
- Showing child-run cost in `adlc workflow status` (it reads only the workflow log; `adlc cost --db` shows the runs).
- Expiry of gates, who may approve (`--by` is recorded as given), notifications.
- `adlc eval --workflow`, `GateAutoApprove` in evals and `Options.SkipExport` (W.6).
- The review loop (W.4); the `workflows/sdlc.yaml` pipeline and its agents (W.6).

## Pitfalls

- Every git call on the person's repository goes through `repoGit` (hooks and fsmonitor off). `update-ref` runs the `reference-transaction` hook if hooks are on.
- Never use the repository's own index: `GIT_INDEX_FILE` must point at the private `export.index` for `read-tree`, `apply --cached` and `write-tree`, and must not be set for any other call.
- The empty last argument of `update-ref` is what makes git refuse an existing branch. Do not drop it, and do not check-then-create without it.
- Export the diff `base..lastCommit(events)` onto the **source commit** (from the base commit's `source-commit:` line), never onto the repo's current HEAD and never the final tree as a whole.
- Pass the patch to `git apply` exactly as `git diff` printed it: do not trim it (a helper that trims stdout drops the final newline and the last hunk no longer applies). If W.3's git helper trims, add an untrimmed variant.
- `checkGate` writes `gate_requested` only when none is pending: a resumed parked workflow must not get a second one.
- `parent_seq` of `gate_resolved` is the seq of that task's `gate_requested`.
- `ResolveGate` checks everything before it appends; a refused call leaves the log unchanged.
- `--input` and `--fake-script` are `StringArrayVar`, not `StringSliceVar` (a value may contain a comma).
- Make `dir`/`file` inputs absolute before `Start`; the export reads `inputs["repo"]` later, maybe from another directory.
- The `--fake-script` key is the agent's `name:`, not the file name.
- Every exported identifier needs a doc comment; `GatePolicy` may already have one from W.3.

## Done checklist

- [ ] `Config.WorkflowID`/`TaskID` written into `run_started`; existing harness tests unchanged
- [ ] `checkGate`, `ResolveGate`, `ErrNoPendingGate`, exact gate summary; `GateAutoApprove` works
- [ ] `runExport` creates `adlc/<id>` from the source commit + the workflow's diff; refuses an existing branch; no hook runs; nothing pushed; the person's index and working tree untouched (tests)
- [ ] The export validation rule added to W.1's `Validate`
- [ ] `adlc workflow run|resume|approve|status` registered, with the exact output and exit codes, and tests
- [ ] Child runs get sandbox tools, hooks, the kill switch, `NAME@N` refs, the nested budget and the workflow/task ids (tests)
- [ ] `adlc approve` continues a workflow child run on its kept sandbox; `adlc workflow resume` then collects it (test)
- [ ] `interfaces.md` §6 and the M3 section updated
- [ ] No change to `events.proto`, `internal/eventlog`, W.2's fold or W.4's `review.go`
- [ ] `make ci` passes
