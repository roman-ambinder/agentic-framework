# W.6 — SDLC agents, the `sdlc` workflow and pipeline evals

Size: M · Depends on: W.4, W.5 · Branch: `task/W.6-sdlc-pipeline` · PR title: `W.6: sdlc workflow with spec and review agents, offline demo and adlc eval --workflow`

## Goal

After W.1–W.5 the engine runs any workflow file, with checks, a review loop,
human gates and export. After this task the repository has the first real
pipeline and can measure it:

- two new agent roles in `agents/sdlc/`: a **spec writer** and a **reviewer**
  (the critic of the review loop), one shared prompt per role and one agent
  file per provider; both may only read the workspace. The coder is the
  existing task 1.10 agent, unchanged;
- `workflows/sdlc.yaml` (Anthropic agents) and `workflows/sdlc-openai.yaml`:
  spec → implement (tests as checks, reviewed by the critic) → human gate →
  export (m3.md "Demo");
- an offline demo, `examples/sdlc-demo/`: the same pipeline with fake-model
  agents and a tiny buggy Go module, so `adlc workflow run` works without an
  API key (with Docker by hand, with the test sandbox in `go test`);
- `adlc eval SUITE_DIR --workflow FILE` (m3.md "Pipeline evals"): the
  workflow runs once per bug and attempt with gates auto-approved and export
  skipped, and the final workspace is graded by the existing graders, so
  `adlc eval-compare` can put the pipeline next to the single coder agent;
- `Options.SkipExport` implemented, failure analysis and the result table
  understand workflow results, and the manual live-eval job can run a workflow.

No schema change, no new dependency, no real model id anywhere: models and
prices come only from `${ADLC_ANTHROPIC_MODEL}`, `${ADLC_OPENAI_MODEL}` and the
existing `${ADLC_*_PRICE_*}` variables.

## Read first

- `AGENTS.md`; `docs/impl/m3.md` (decisions 6, 8, 9, 10, 11, 13 and "Pipeline evals"); `docs/impl/live-eval.md`
- `docs/impl/tasks/W.1-workflow-file.md` … `W.5-gates-cli.md` (W.5's `workflowWiring`, `workflowRun`, `workflowApprove`, `workflowSpend`, `openWorkflowEngine` are used here)
- `docs/impl/tasks/1.10-coder-agent.md` (agent file shape, prompt style, tool list); `agents/sdlc/coder.anthropic.yaml`, `coder.openai.yaml`, `coder.prompt.md`
- `docs/impl/tasks/C.2-policy-v2.md` (rule fields), `C.4-registry.md` (`Result.AgentDigest`), `Q.1-eval-compare.md` (`Repeat`, `Attempt`, `--map`), `Q.2-failure-analysis.md` (`Classify`, `AnalyzeFailures`)
- `docs/impl/interfaces.md` §7, §8 (tool names and risks), §9, §11
- `internal/evals/runner.go` (`Runner`, `runAttempt`, `Result.run`, `grade`, `changedFiles`), `grade.go` (`gradeOf`, `trajectory`), `failures.go` (`Classify`, `classifyGrade`, `AnalyzeFailures`), `report.go` (`stopWord`, `PrintTable`), `workspace.go` (`PrepareWorkspace`, `copyDir`), `runner_test.go` (`newRunnerEnv`, `fixCmd`)
- `internal/cli/eval.go` (`runEval`, `evalDryRun`, `loadEvalInputs`, `evalWorkDir`), `eval_test.go` (`writeBudgetAgent`, `miniSuite`), `workflow.go` (W.5)
- `internal/workflow/export.go` (W.5's `runExport`)
- `internal/agentconfig/repo_agents_test.go` (`TestLoadRepoAgents` walks `agents/`)
- `.github/workflows/eval-live.yml`; `examples/hello.fake.json` (fake script format)

## Files

Create:

```
agents/sdlc/spec.prompt.md
agents/sdlc/spec.anthropic.yaml
agents/sdlc/spec.openai.yaml
agents/sdlc/reviewer.prompt.md
agents/sdlc/reviewer.anthropic.yaml
agents/sdlc/reviewer.openai.yaml
workflows/sdlc.yaml
workflows/sdlc-openai.yaml
examples/sdlc-demo/workflow.yaml
examples/sdlc-demo/spec.fake.yaml
examples/sdlc-demo/coder.fake.yaml
examples/sdlc-demo/reviewer.fake.yaml
examples/sdlc-demo/spec.fake.json
examples/sdlc-demo/coder.fake.json
examples/sdlc-demo/reviewer.fake.json
examples/sdlc-demo/issue.md
examples/sdlc-demo/repo/go.mod
examples/sdlc-demo/repo/greet/greet.go
examples/sdlc-demo/repo/greet/greet_test.go
internal/evals/pipeline.go          WorkflowProvider, WorkflowRun, WorkflowResult, Pipeline, Runner.RunPipeline
internal/evals/pipeline_test.go
internal/cli/eval_workflow.go       adlc eval --workflow
internal/cli/eval_workflow_test.go
internal/cli/sdlc_test.go           repo workflows load; the offline demo runs
```

Change:

```
internal/evals/runner.go            Runner.RunWorkflow field; extract gradeWorkspace from Result.run (no behaviour change)
internal/evals/failures.go          AnalyzeFailures: workflow results are classified without reading run events
internal/evals/report.go            stopWord also trims "WORKFLOW_OUTCOME_"
internal/workflow/engine.go         the field Options.SkipExport
internal/workflow/export.go         SkipExport at the top of runExport
internal/workflow/export_test.go    one test
internal/cli/eval.go                --workflow, --workflow-db, --control-db; dispatch; shared report tail
internal/cli/workflow.go            eachAgent helper (checkAgents uses it; no behaviour change)
internal/cli/cli_test.go            eval in the shared --control-db and --workflow-db tests
.github/workflows/eval-live.yml     input "workflow"
docs/impl/live-eval.md              how to run the pipeline live
docs/impl/interfaces.md             §9 line; a subsection appended to §12 (shared file)
docs/design/2026-11-13-adlc-framework-design.md   §4 table: Spec and Release rows (reality differs)
README.md                           "Run the SDLC workflow" section
```

`agents/sdlc/coder.*` are not changed. `Makefile` and `.github/workflows/ci.yml` are not touched.

## Design

### Roles and tools

The workflow tasks get the seven sandbox tools (interfaces.md §8): every
child run gets the same in-process router (W.5 `runTask`). What a role may do
is decided by its policy:

| Role | Allowed (risk) | Denied | Why |
|------|----------------|--------|-----|
| spec writer | `git__status`, `git__diff`, `search__grep`, `files__read_file`, `files__list_dir` (READ) | `shell__run` (EXEC), `files__write_file` (WRITE_LOCAL) | design §4: "search, fs(read)". It needs no program run to read code, and `shell__run` could write files |
| coder | all seven (task 1.10, unchanged) | — | it must change code and run tests |
| reviewer | the same READ tools | `shell__run`, `files__write_file` | design §4: "git, search". The build and tests already passed as checks before the critic runs (decision 9), so it has nothing to run |

A denied call ends the run `DENIED` (§6.2), which fails that attempt. The
prompts therefore list only the allowed tools and say plainly that the other
two end the run. The deny rules are explicit (not only `default: deny`) so the
stop detail names the rule (`rule no-exec: ...`), which `adlc trace` and the
failure analysis show.

**Decision: the spec is the spec writer's final reply, not a file.** The next
task reads it with `{{ result "spec" }}` (decision 6). Design §4 calls the
artifact `spec.md`; writing it into the workspace would need a write rule for
one path, would put `spec.md` into the exported branch and into the graded
diff (one more changed non-test file than the single coder agent makes, which
skews the comparison of decision 13), and the coder could edit it. The final
reply is recorded in the spec run's log, which is the audit trail.

### Agent files

`agents/sdlc/spec.anthropic.yaml` (exact):

```yaml
name: spec-anthropic
version: 1
system_file: spec.prompt.md           # relative to this file
model:
  provider: anthropic
  name: ${ADLC_ANTHROPIC_MODEL}       # set in your shell or as a repository variable; not committed
  max_tokens: 8000
  effort: medium
  # Prices: USD per million tokens, from the ADLC_ANTHROPIC_PRICE_* variables
  # (see docs/impl/live-eval.md). Not committed: they depend on the model.
  pricing:
    input_per_mtok: ${ADLC_ANTHROPIC_PRICE_INPUT}
    output_per_mtok: ${ADLC_ANTHROPIC_PRICE_OUTPUT}
    cache_read_per_mtok: ${ADLC_ANTHROPIC_PRICE_CACHE_READ}
    cache_write_per_mtok: ${ADLC_ANTHROPIC_PRICE_CACHE_WRITE}
budget:
  max_steps: 25
  max_tokens: 1000000
  max_wall_clock: 10m
  max_usd: "0.50"
  max_tool_calls: 60
policy:
  default: deny
  rules:
    - {id: no-exec,  risk: exec,        decision: deny}
    - {id: no-write, risk: write_local, decision: deny}
    - {id: read,     risk: read,        decision: allow}
```

`spec.openai.yaml`: identical except `name: spec-openai`, `provider: openai`,
`name: ${ADLC_OPENAI_MODEL}` and the four `${ADLC_OPENAI_PRICE_*}` variables.

`agents/sdlc/reviewer.anthropic.yaml`: identical to `spec.anthropic.yaml`
except `name: reviewer-anthropic`, `system_file: reviewer.prompt.md`, and
`budget: {max_steps: 20, max_tokens: 800000, max_wall_clock: 10m, max_usd: "0.40", max_tool_calls: 50}`
(written as a block like above). `reviewer.openai.yaml`: the OpenAI variant of it
(`name: reviewer-openai`).

### Spec writer prompt (`agents/sdlc/spec.prompt.md`, exact)

```markdown
You are a senior Go engineer writing a short implementation spec for a fix to a reported issue.
The repository is in /workspace. You can read it but you must not change it: another engineer will make the change from your spec.

Tools you may use:
- git__status: Show changed, staged and untracked files in the workspace (git status).
- git__diff: Show line changes in tracked files (git diff).
- search__grep: Search file contents in the workspace for a regular expression.
- files__read_file: Read a text file from the workspace.
- files__list_dir: List the entries of a directory in the workspace.

shell__run and files__write_file are not allowed for you. Calling either one ends your work with an error. Never call them.

Work in this order:
1. Read the issue. Write down, in one or two sentences, what behaviour is wrong and what the correct behaviour is.
2. Explore: list the files, find the code involved, read it. Follow the calls until you find where the wrong behaviour comes from.
3. Write the spec as your final reply, without calling any tool, using exactly these five headings:

## Problem
What is wrong and what the correct behaviour is, in terms of the code's behaviour, not its implementation.
## Cause
The file and function where the wrong behaviour comes from, and why it happens.
## Change
A short list: for each file to change, the function and what to change. The smallest change that fixes the behaviour; nothing else.
## Test
One test that fails before the fix and passes after it: its package, the file name <package>_issue_test.go, the test name, the input and the expected result.
## Acceptance criteria
A short list of checks a reviewer can verify: the behaviour from the issue, that existing tests still pass, and that `go build ./...` and `go test ./...` pass.

Rules:
- Name only files and functions you have read. If you are not sure where the cause is, say which places you checked and which one is most likely.
- Describe the fix; do not write its code.
- Keep the spec under 60 lines.
- Do not download anything; there is no network.
- If a tool call fails, read the error before trying again. Do not repeat the same failing call unchanged.
```

### Reviewer prompt (`agents/sdlc/reviewer.prompt.md`, exact)

The verdict line is the contract of the review loop (m3.md decision 9; W.4's
`parseVerdict` reads the last non-empty line). W.4's `criticPromptFormat` (the
critic's task message) already asks for it; the system prompt repeats it and
forbids text after it, and describes the inputs exactly as W.4 sends them.

```markdown
You are a senior Go engineer reviewing a change that another engineer made to fix a reported issue.
The repository, with the change applied, is in /workspace. You can read it but you must not change it.
Your task message gives you the task the author worked on (it contains the issue), the whole change as a diff, and the checks that passed on it.

Tools you may use:
- git__status: Show changed, staged and untracked files in the workspace (git status).
- git__diff: Show line changes in tracked files (git diff).
- search__grep: Search file contents in the workspace for a regular expression.
- files__read_file: Read a text file from the workspace.
- files__list_dir: List the entries of a directory in the workspace.

shell__run and files__write_file are not allowed for you. Calling either one ends your review with an error. Never call them.
The checks listed in your task message have already been run on this change and they passed; you do not need to run them.

Review in this order:
1. Read the task and the issue in it, and write down, in one sentence, the behaviour that must change.
2. Read the diff. For each changed file, open it and read the code around the change.
3. Check each point:
   a. The change fixes the behaviour described in the issue in general, not only the case in the new test.
   b. There is a new or changed test that fails without the fix.
   c. Nothing unrelated changed: no renames, reformatting or rewrites of code the issue does not need.
   d. No existing test was weakened or deleted, unless the issue says it is wrong.
   e. No new bug: nil dereference, off-by-one, ignored error, or changed behaviour the issue did not ask for.
4. Decide. Ask for changes only for a problem under a to e that you can point to in the code. Do not ask for changes of style or for things you would merely have done differently.

Your final reply, without calling any tool, has exactly this form:

Findings:
- <file>:<function or line>: <the problem and the change you want>
(one line per finding; write "- none" when there are none)

and then, as the very last line, exactly one of these two lines:
VERDICT: APPROVE
VERDICT: CHANGES

Write VERDICT: CHANGES only when at least one finding needs a code change. Never write anything after the verdict line.
```

### `workflows/sdlc.yaml` (exact)

```yaml
# The SDLC pipeline (m3.md): spec -> implement (tests as checks, reviewed) -> human gate -> export.
# Agents use the Anthropic provider; workflows/sdlc-openai.yaml is the same pipeline with OpenAI.
name: sdlc
version: 1
inputs:
  - name: repo
    kind: dir
  - name: issue
    kind: file
budget:
  max_usd: 3.00
  max_tokens: 6000000
  max_wall_clock: 1h
tasks:
  - id: spec
    agent: ../agents/sdlc/spec.anthropic.yaml
    attempts: 2
    prompt: |
      Write an implementation spec for the issue below. The repository is in /workspace.

      Issue:
      {{ input "issue" }}
  - id: implement
    agent: ../agents/sdlc/coder.anthropic.yaml
    depends_on: [spec]
    prompt: |
      Fix the issue below in the repository in /workspace.
      Another engineer read the code and wrote the spec below. Follow it unless the code shows that it is wrong; if it is, say so in your final reply.

      Issue:
      {{ input "issue" }}

      Spec:
      {{ result "spec" }}
    checks:
      - go build ./...
      - go test ./...
    attempts: 2
    budget:
      max_usd: 1.50
    review:
      critic: ../agents/sdlc/reviewer.anthropic.yaml
      max_rounds: 2
  - id: release
    kind: export
    depends_on: [implement]
    gate: approval
```

`workflows/sdlc-openai.yaml`: identical except the comment's second line
(`# Agents use the OpenAI provider; workflows/sdlc.yaml is the same pipeline with Anthropic.`),
`name: sdlc-openai`, and the three agent paths `spec.openai.yaml`,
`coder.openai.yaml`, `reviewer.openai.yaml`.

**This is the real file; m3.md's workflow example is illustrative.** Where
they overlap they agree (inputs, task ids, agents, checks, `attempts: 2` and
`budget: {max_usd: 1.50}` on `implement`, `max_rounds: 2`, the gated export,
workflow `max_usd: 3.00`, `{{ input "issue" }}` → spec, `{{ result "spec" }}` →
implement, `max_tokens: 6000000`, which stays above the coder's own
3,000,000-token budget). It differs on purpose in: `attempts: 2` on `spec`, and fuller prompt texts
(the implement prompt also gets the issue).

Why these numbers: one coder run may cost up to $2.00 (its own budget); the
task narrows it to $1.50 so that spec, review and a retry fit in the
workflow's $3.00. The workflow budget is what `--dry-run` multiplies (below).
`{{ input "issue" }}` is the issue file's content (W.1/W.3: a `file` input
renders as its content); `{{ result "spec" }}` needs `depends_on: [spec]`
(W.1 rule 25).

### The offline demo (`examples/sdlc-demo/`)

The demo is the same pipeline with three fake-provider agents and a tiny
module with one bug: `Hello("")` returns `Hello, !` instead of `Hello, world!`.

`workflow.yaml`: exactly `workflows/sdlc.yaml` with these differences:
first comment line `# Offline demo of workflows/sdlc.yaml: fake models, examples/sdlc-demo/repo as the repository.`
(no second line), `name: sdlc-demo`, `budget: {max_wall_clock: 10m}` (as a block),
agents `spec.fake.yaml`, `coder.fake.yaml`, `reviewer.fake.yaml` (same directory),
no `attempts:` lines, no task `budget:`, and `max_rounds: 1`. The `prompt:`,
`checks:`, `depends_on:`, `kind:` and `gate:` values are the same text
(`TestDemoMatchesSDLCWorkflow` checks this).

`spec.fake.yaml` (the real prompt, so the demo shows what the model would get):

```yaml
name: spec-fake
version: 1
system_file: ../../agents/sdlc/spec.prompt.md
model:
  provider: fake
budget:
  max_steps: 5
  max_tool_calls: 5
  max_wall_clock: 2m
policy:
  default: deny
  rules:
    - {id: no-exec,  risk: exec,        decision: deny}
    - {id: no-write, risk: write_local, decision: deny}
    - {id: read,     risk: read,        decision: allow}
```

`reviewer.fake.yaml`: the same with `name: reviewer-fake` and
`system_file: ../../agents/sdlc/reviewer.prompt.md`. `coder.fake.yaml`:
`name: coder-fake`, `system_file: ../../agents/sdlc/coder.prompt.md`, the same
budget block, and the coder's policy (`read`, `write`, `exec` allowed, task 1.10).

`spec.fake.json`:

```json
{
  "turns": [
    {"text": "I'll read the greet package.",
     "tool_calls": [{"id": "call_1", "name": "files__read_file", "arguments": {"path": "greet/greet.go"}}],
     "finish": "tool_use", "usage": {"input_tokens": 400, "output_tokens": 30}},
    {"text": "## Problem\nHello(\"\") returns \"Hello, !\"; it must return \"Hello, world!\".\n## Cause\ngreet/greet.go, Hello: the name is used as given, also when it is empty.\n## Change\n- greet/greet.go, Hello: use \"world\" when name is empty.\n## Test\nPackage greet, file greet/greet_issue_test.go, TestHelloEmptyName: Hello(\"\") must be \"Hello, world!\".\n## Acceptance criteria\n- Hello(\"\") returns \"Hello, world!\".\n- Existing tests pass; go build ./... and go test ./... pass.",
     "finish": "end_turn", "usage": {"input_tokens": 600, "output_tokens": 120}}
  ]
}
```

`coder.fake.json`:

```json
{
  "turns": [
    {"text": "I'll fix Hello and add a test.",
     "tool_calls": [
       {"id": "call_1", "name": "files__write_file", "arguments": {"path": "greet/greet.go", "content": "// Package greet builds greetings.\npackage greet\n\n// Hello returns a greeting for name. An empty name greets the world.\nfunc Hello(name string) string {\n\tif name == \"\" {\n\t\tname = \"world\"\n\t}\n\treturn \"Hello, \" + name + \"!\"\n}\n"}},
       {"id": "call_2", "name": "files__write_file", "arguments": {"path": "greet/greet_issue_test.go", "content": "package greet\n\nimport \"testing\"\n\nfunc TestHelloEmptyName(t *testing.T) {\n\tif got := Hello(\"\"); got != \"Hello, world!\" {\n\t\tt.Fatalf(\"got %q, want %q\", got, \"Hello, world!\")\n\t}\n}\n"}}],
     "finish": "tool_use", "usage": {"input_tokens": 900, "output_tokens": 200}},
    {"text": "Running the tests.",
     "tool_calls": [{"id": "call_3", "name": "shell__run", "arguments": {"command": "go test ./..."}}],
     "finish": "tool_use", "usage": {"input_tokens": 1200, "output_tokens": 20}},
    {"text": "Cause: Hello used an empty name as given. Changed greet/greet.go; added TestHelloEmptyName in greet/greet_issue_test.go.",
     "finish": "end_turn", "usage": {"input_tokens": 1400, "output_tokens": 40}}
  ]
}
```

`reviewer.fake.json`:

```json
{
  "turns": [
    {"text": "Findings:\n- none\nVERDICT: APPROVE",
     "finish": "end_turn", "usage": {"input_tokens": 800, "output_tokens": 10}}
  ]
}
```

`issue.md`:

```markdown
# Hello greets nobody when the name is empty

`greet.Hello("")` returns `Hello, !`. With an empty name it should greet the
world: `Hello, world!`. Other names must keep working as before
(`Hello("Ada")` is `Hello, Ada!`).
```

`repo/go.mod`: `module example.com/greet` + blank line + `go 1.22` (an old
directive on purpose: the test sandbox runs the host's `go` with
`GOTOOLCHAIN=local`). It is a separate module, so the repository's own
`go build ./...`, `go vet` and lint never see it.

`repo/greet/greet.go`:

```go
// Package greet builds greetings.
package greet

// Hello returns a greeting for name. An empty name greets the world.
func Hello(name string) string {
	return "Hello, " + name + "!"
}
```

`repo/greet/greet_test.go`:

```go
package greet

import "testing"

func TestHelloName(t *testing.T) {
	if got := Hello("Ada"); got != "Hello, Ada!" {
		t.Fatalf("got %q, want %q", got, "Hello, Ada!")
	}
}
```

The demo repository must be a git repository of its own for the export
(W.5 checks `repo` is the top level). `examples/sdlc-demo/repo` is inside
this repository, so the demo and the test copy it to a temporary directory
and `git init` it there.

### `Options.SkipExport` (`internal/workflow/engine.go`, `export.go`)

W.3 left the field out of `Options` (its brief assigns it here). Add it after W.5's `Gates`:

```go
	SkipExport bool // task W.6: export tasks pass without exporting (evals)
```

At the top of W.5's `runExport`: when `e.o.SkipExport` is true, append
`task_started{task_id, role EXPORT, attempt 1}` and
`task_finished{task_id, role EXPORT, attempt 1, outcome PASSED, detail "export skipped", commit ""}`
(parent = the `task_started`) and return. No git command runs.

### Pipeline evals in `internal/evals` (`pipeline.go`)

The grading code is unexported in `evals`, and `evals` cannot import
`internal/workflow` (the engine uses `evals.DefaultImage`), so the eval loop
for workflows lives in `evals` and the engine is reached through a callback,
the same way `Runner.RunAgent` reaches the harness.

```go
// WorkflowProvider is Result.Provider of a pipeline result (task W.6).
const WorkflowProvider = "workflow"

// WorkflowRun is what RunWorkflow needs to run a workflow on one bug.
type WorkflowRun struct {
	ID        string // workflow id; also Result.RunID
	Bug       Bug
	RepoDir   string // host dir with the prepared buggy repository and its .git (input "repo")
	IssuePath string // host path of the bug's issue.md (input "issue")
}

// WorkflowResult is what RunWorkflow returns.
type WorkflowResult struct {
	Outcome   string             // adlcv1.WorkflowOutcome name, e.g. "WORKFLOW_OUTCOME_COMPLETED"
	Workspace string             // host dir of the workflow's final workspace
	State     *eventlog.RunState // the child runs summed (W.5 workflowSpend), for the grade's trajectory
}

// Pipeline names the workflow in the results.
type Pipeline struct {
	Agent  string                  // Result.Agent: "<name>@<version>"
	Digest string                  // Result.AgentDigest: Workflow.Digest
	NewID  func(now time.Time) string // a new workflow id (Workflow.ID)
}

// RunPipeline runs the workflow on every bug, Repeat times each, bugs in
// order, and grades each final workspace like RunAll grades an agent's. It
// returns an error only when ctx is cancelled (with the results so far) or
// when RunWorkflow is nil.
func (r *Runner) RunPipeline(ctx context.Context, bugs []Bug, p Pipeline) ([]Result, error)
```

`Runner` gets one field, after `RunAgent`:

```go
	// RunWorkflow runs a workflow for one bug (task W.6); the CLI wires the
	// engine, tests pass a fake. Used only by RunPipeline.
	RunWorkflow func(ctx context.Context, wr WorkflowRun) (WorkflowResult, error)
```

**Refactor in `runner.go` (no behaviour change):** move the end of
`Result.run`, from `res.ChangedFiles = …` to the end, into

```go
// gradeWorkspace records the changed files and the grade of the workspace
// after (compared with the base commit in before/.git).
func (res *Result) gradeWorkspace(ctx context.Context, r *Runner, bug Bug, spec sandbox.Spec, before, after string)
```

`Result.run` ends with `res.gradeWorkspace(ctx, r, bug, spec, before, after)`.
All existing runner tests pass unchanged.

**`RunPipeline` algorithm**, per bug and attempt (`attempt` 1..`max(1, r.Repeat)`), checking `ctx.Err()` before each:

1. `start := r.now()`; `id := p.NewID(start)` (no `-a<n>` suffix: the workflow id, `<name>-v<version>-<time>-<6 hex>` per m3.md decision 14, is already unique; with the 6-character `-grade` suffix the grading sandbox's run id stays within 64 characters).
2. `res = Result{BugID, Difficulty, Agent: p.Agent, Provider: WorkflowProvider, AgentDigest: p.Digest, RunID: id, Attempt: attempt}`;
   the same deferred `DurationMS` and progress line as `runAttempt`.
3. `base := filepath.Join(r.WorkDir, id)`; `before`, `after` = `base/before`, `base/after`.
   `r.Suite.PrepareWorkspace(ctx, bug, before, PrepareOptions{})`; error → `res.Error = "prepare workspace: " + err`.
4. `out, err := r.RunWorkflow(ctx, WorkflowRun{ID: id, Bug: bug, RepoDir: before, IssuePath: filepath.Join(bug.Dir, "issue.md")})`;
   `res.State = out.State`; `res.StopReason = out.Outcome`; error → `res.Error = "run workflow: " + err`.
5. `copyDir(out.Workspace, after)` (skips `.git`, refuses symlinks); error → `res.Error = "copy workspace: " + err`.
6. `spec := sandbox.Spec{RunID: id, Image: <r.Image or DefaultImage>, WorkspaceDir: after, CPUs: 2, MemoryBytes: 4 << 30, Env: <the runner's Go env, W.3's evals.GoEnv()>}`;
   `res.gradeWorkspace(ctx, r, bug, spec, before, after)`.

`before` keeps its `.git` (the engine copies `repo` into the workspace without
it, m3.md decision 4) so `changedFiles` diffs the final workspace against the
buggy base exactly as for a single agent. The hidden test is written only into
`after`, after the workflow has finished: no agent ever sees it.

**`report.go`**: `stopWord` trims `"WORKFLOW_OUTCOME_"` as well as
`"STOP_REASON_"`, so the table shows `COMPLETED`, `FAILED`, `REVIEW_EXHAUSTED`.

**`failures.go`**: in `AnalyzeFailures`, a result with `Provider == WorkflowProvider`
is classified by `classifyWorkflow(r)` and its events are never read (its
`RunID` is a workflow id, which is not in the run log). `Classify` is unchanged.

```go
// classifyWorkflow labels an unsolved pipeline result.
func classifyWorkflow(r Result) (FailureCause, string)
```

| Condition (in order) | Cause | Evidence |
|----------------------|-------|----------|
| `r.Error != ""` | `CauseInfra` | `oneLine(r.Error)` |
| `StopReason == "WORKFLOW_OUTCOME_BUDGET_EXHAUSTED"` | `CauseBudget` | `workflow budget exhausted` |
| `StopReason == "WORKFLOW_OUTCOME_CANCELLED"` | `CauseInfra` | `workflow cancelled` |
| `StopReason` is `""` or `"WORKFLOW_OUTCOME_UNSPECIFIED"` | `CauseRefusedOrError` | `workflow did not complete` |
| otherwise | `classifyGrade(r, &eventlog.RunState{})` | its evidence; when the outcome is not `COMPLETED`, prefixed with `<outcome lower case without prefix>; ` (e.g. `review_exhausted; no _test.go file changed (1 files changed)`) |

### `adlc eval --workflow` (`internal/cli/eval_workflow.go`, `eval.go`)

```
adlc eval SUITE_DIR --workflow FILE [--bugs ...] [--repeat N] [--db] [--workflow-db] [--control-db]
                    [--out] [--workdir] [--image] [--keep-workspaces] [--dry-run]
```

SUITE_DIR stays positional (m3.md writes the command without it; the suite is
still needed). New flags on `adlc eval`:

```go
f.StringVar(&o.workflow, "workflow", "", "workflow file to evaluate instead of --agents")
f.StringVar(&o.workflowDB, "workflow-db", defaultWorkflowDBPath, "workflow log (with --workflow)")
f.StringVar(&o.controlDB, "control-db", defaultControlDBPath, "control database: kill switches, NAME@N agents (with --workflow)")
```

`--agents` is no longer `MarkFlagRequired`. In `runEval`, after the
`--repeat` check: both or neither of `--agents`/`--workflow` →
`eval: give either --agents or --workflow` (exit 1; `TestEvalRequiresAgents`
still sees `agents`). `--workflow` set → `return runWorkflowEval(ctx, o, stdout)`.

Move the end of `runEval` (from `rep := evals.Report{…}` to the end) into
`finishEval(stdout io.Writer, o evalOptions, suite string, started time.Time, results []evals.Result, runErr error) error`;
both paths call it. `adlc eval --agents` behaves exactly as before.

**`loadEvalWorkflow(ctx, o) (*workflow.Workflow, workflowWiring, error)`** (no Docker):

1. `w := workflow.Load(o.workflow)`.
2. Inputs: exactly `repo` of kind `dir` and `issue` of kind `file`, else
   `eval: workflow %s must declare exactly the inputs repo (dir) and issue (file)` (the file path).
3. `ww := workflowWiring{wf: w, controlDB: o.controlDB}`; for every agent and critic
   (W.5 `eachAgent`, below): provider `fake` (primary, a fallback or the plan role) →
   `eval: workflow %s: task %s: agent %s: provider fake is not supported by adlc eval`.

Add to W.5's `workflow.go`, and make `checkAgents` use it:

```go
// eachAgent loads every agent and critic of the workflow (tasks in file
// order, the task's agent before its critic) and calls fn with the task id,
// the reference as written and the loaded agent.
func (ww workflowWiring) eachAgent(ctx context.Context, fn func(taskID, ref string, a *agentconfig.Agent) error) error
```

**Dry run**: `workflowDryRun(stdout, bugs, w, repeat) error`:
`w.Budget.MaxUSDMicros == 0` → `eval: workflow %s@%d has no budget.max_usd, worst case is unbounded` (exit 1);
else print exactly `bugs=%d workflow=%s@%d repeat=%d worst_case_usd=%d.%06d\n` with
`total = MaxUSDMicros × bugs × repeat`. The workflow budget bounds the sum of
every child run (decision 7) the way an agent's `max_usd` bounds one run; like
there, the last model call of a run may go a little over. The live-eval spend
guard parses the text after `worst_case_usd=`, so it works unchanged.

**`runWorkflowEval(ctx, o, stdout)`**:

1. `suite := evals.LoadSuite(o.suiteDir)`; `bugs := selectBugs(suite, o.bugs)`; `w, ww := loadEvalWorkflow(ctx, o)`. Any error → exit 1.
2. `o.dryRun` → `workflowDryRun`.
3. Open like `runEval`: the run log `o.db` (mkdir), the workflow log `o.workflowDB` (mkdir, `WithFirstKind("workflow_started")`),
   `ks := openKillSwitch(ctx, o.controlDB)`, `docker.New` (same "Docker is not available" line), `evalWorkDir(o)`.
4. `ww.runs = store; ww.killSwitch = ks`.
5. `eng := workflow.New(workflow.Options{Log: wlog, Runs: store, Sandbox: backend, Image: o.image,
   BaseDir: filepath.Join(workDir, "workflows") (m3.md "Pipeline evals": the eval work directory, so `--keep-workspaces` applies), RunTask: ww.runTask, AgentBudget: ww.agentBudget,
   Now: time.Now, Stdout: stdout, Gates: workflow.GateAutoApprove, SkipExport: true})`.
6. `r := &evals.Runner{Suite: suite, Sandbox: backend, Log: store, Image: o.image, WorkDir: workDir, Stdout: stdout,
   Repeat: o.repeat, RunWorkflow: evalRunWorkflow(eng, w, store)}`.
7. `results, runErr := r.RunPipeline(ctx, bugs, evals.Pipeline{Agent: fmt.Sprintf("%s@%d", w.Name, w.Version), Digest: w.Digest, NewID: w.ID})`.
8. `return finishEval(stdout, o, suite.Name, started, results, runErr)`.

```go
// evalRunWorkflow starts one workflow per bug and reports where its final
// workspace is and what its child runs used.
func evalRunWorkflow(eng *workflow.Engine, w *workflow.Workflow, runs *eventlog.Store) func(context.Context, evals.WorkflowRun) (evals.WorkflowResult, error)
```

Body: `st, err := eng.Start(ctx, w, wr.ID, map[string]string{workflow.ExportInput: wr.RepoDir, "issue": wr.IssuePath})`;
error → return it. `agg, _, err := workflowSpend(ctx, runs, st)`. `Outcome` = `st.Outcome.String()` when
`st.Status == "completed"`, else `adlcv1.WorkflowOutcome_WORKFLOW_OUTCOME_UNSPECIFIED.String()` (a child
parked for a tool approval; evals cannot answer it). `Workspace` = `eng.Workspace(wr.ID)`.

How the `Result` fields are filled for a pipeline (m3.md, plus the open ones):

| Field | Value |
|-------|-------|
| `Agent` | `<name>@<version>`, e.g. `sdlc@1` (m3.md) |
| `Provider` | `"workflow"` (m3.md) |
| `AgentDigest` | `Workflow.Digest` — see below |
| `RunID` | the workflow id (m3.md); `adlc workflow status <id> --workflow-db …` and the `RUNS` column lead to the child runs for `adlc trace` |
| `Attempt` | 1..repeat (Q.1) |
| `StopReason` | the `WorkflowOutcome` name, e.g. `WORKFLOW_OUTCOME_COMPLETED` |
| `Grade` trajectory | the sums of every child run: `Steps`, `ModelCalls`, `ToolCalls`, `WastedCalls`, `Denials`, tokens and `USDMicros`; `WallClockMS` from the workflow's first to last event |
| `Hidden`, `Grade` outcome, `ChangedFiles` | exactly as for one agent, from the final workspace |

**Decision: `AgentDigest` = `Workflow.Digest`.** It is the digest
`workflow_started.workflow_digest` records, so a report row and its workflow
log can be matched; `adlc agent promote` compares result digests with agent
versions, which a workflow digest never matches, so a pipeline report can
never promote an agent by mistake. It does not cover the agent files the
workflow names: changing an agent's prompt without changing its `version`
leaves the digest the same. Agent versions are recorded per child run
(`agent_version`), and the registry (C.4) is the way to pin them
(`agent: coder-anthropic@3`).

Comparing with the single coder (decision 13):

```bash
./bin/adlc eval evals/suites/go-bugs --agents agents/sdlc/coder.anthropic.yaml --out evals/results/coder.json
./bin/adlc eval evals/suites/go-bugs --workflow workflows/sdlc.yaml --out evals/results/sdlc.json
./bin/adlc eval-compare evals/results/coder.json evals/results/sdlc.json --map coder-anthropic@1=sdlc@1
```

### Live eval (`.github/workflows/eval-live.yml`)

**Decision: add it now.** It is a few lines, the spend guard works unchanged
(the dry-run line ends with `worst_case_usd=`), and the failure step works
because `classifyWorkflow` never reads run events. Changes, exactly:

- `on.workflow_dispatch.inputs`, after `agents`:
  ```yaml
      workflow:
        description: Workflow file; when set it is evaluated instead of the agents (for example workflows/sdlc.yaml)
        default: ""
  ```
- `jobs.eval.env`, after `INPUT_AGENTS`: `INPUT_WORKFLOW: ${{ inputs.workflow }}`
- step `spend guard`: replace the `args=(...)` line with
  ```bash
          if [ -n "$INPUT_WORKFLOW" ]; then
            args=(evals/suites/go-bugs --workflow "$INPUT_WORKFLOW" --repeat "$INPUT_REPEAT" --dry-run)
          else
            args=(evals/suites/go-bugs --agents "$INPUT_AGENTS" --repeat "$INPUT_REPEAT" --dry-run)
          fi
  ```
- step `eval`: replace the `args=(...)` line with
  ```bash
          if [ -n "$INPUT_WORKFLOW" ]; then
            args=(evals/suites/go-bugs --workflow "$INPUT_WORKFLOW" --workflow-db eval-workflows.db --repeat "$INPUT_REPEAT" --db eval.db --out eval-report.json)
          else
            args=(evals/suites/go-bugs --agents "$INPUT_AGENTS" --repeat "$INPUT_REPEAT" --db eval.db --out eval-report.json)
          fi
  ```
- step `upload results`, `path:`: add the line `eval-workflows.db`.

`docs/impl/live-eval.md`, "Start a run": add the bullet

> - `workflow`: a workflow file, for example `workflows/sdlc.yaml`. When set,
>   `agents` is ignored and each bug runs through the whole pipeline (gates
>   are approved automatically, nothing is exported). The worst case is bugs ×
>   `repeat` × the workflow's `budget.max_usd` ($3.00 for `sdlc.yaml`). The
>   artifact also holds `eval-workflows.db`; `adlc workflow status <run_id>
>   --workflow-db eval-workflows.db` lists the child runs of a result, and
>   `adlc trace <child run id> --db eval.db` shows one.

and, under "Read the results", the `eval-compare … --map coder-anthropic@1=sdlc@1` example above.

### Docs

`README.md`, new section after "Run the example" (exact):

````markdown
## Run the SDLC workflow

`adlc workflow run` takes an issue through a pipeline of agents: a spec
writer, the coder (with `go build` and `go test` as checks), a reviewer, and
a human approval before the change becomes a local branch `adlc/<workflow id>`
in your repository (never pushed). The demo uses fake models and needs Docker,
no API key:

```bash
make build
demo=$(mktemp -d) && cp -R examples/sdlc-demo/repo/. "$demo"
git -C "$demo" init -q -b main && git -C "$demo" add -A \
  && git -C "$demo" -c user.name=demo -c user.email=demo@localhost commit -q -m base
d=examples/sdlc-demo
./bin/adlc workflow run $d/workflow.yaml --input repo="$demo" --input issue=$d/issue.md \
  --fake-script spec-fake=$d/spec.fake.json --fake-script coder-fake=$d/coder.fake.json \
  --fake-script reviewer-fake=$d/reviewer.fake.json --db bin/demo.db --workflow-db bin/demo-workflows.db
# status: awaiting_approval; the next: line has the approve command
./bin/adlc workflow approve <workflow id> release $d/workflow.yaml --db bin/demo.db \
  --workflow-db bin/demo-workflows.db --fake-script spec-fake=$d/spec.fake.json \
  --fake-script coder-fake=$d/coder.fake.json --fake-script reviewer-fake=$d/reviewer.fake.json
git -C "$demo" log --oneline adlc/<workflow id>
```

With real models: `workflows/sdlc.yaml` (set `ADLC_ANTHROPIC_MODEL` and the
`ADLC_ANTHROPIC_PRICE_*` variables, see `docs/impl/live-eval.md`).
````

`docs/design/2026-11-13-adlc-framework-design.md` §4 table, two rows change
(reality differs; AGENTS.md "Definition of done"):

```
| Spec | Spec writer | search, fs(read) | Spec (final reply, passed to the coder's prompt) | Human approval (optional) |
| Release | — (export task, no agent) | host git, hooks off | Local branch `adlc/<workflow id>`; the person pushes and opens the PR | **Human approval required** |
```

`interfaces.md` §9, after the Q.2 line:
`// task W.6: WorkflowProvider, WorkflowRun, WorkflowResult, Pipeline, Runner.RunWorkflow, Runner.RunPipeline`;
at the end of §12 "M3 additions" (W.1 creates it; append after the other
subsections, do not reorder), a `### Pipeline evals (W.6)` subsection with the Go block
of "Pipeline evals in `internal/evals`" above and the line
"`Options.SkipExport`: an export task writes `task_started` and
`task_finished{PASSED, "export skipped"}` and runs no git command."

## Steps

1. Branch from the latest `main` (AGENTS.md). Check that W.4 and W.5 are merged and that `workflowWiring`, `workflowSpend`, `workflowRun`, `workflowApprove` exist with the signatures of brief W.5; if not, stop and report.
2. Agent files and prompts; `go test ./internal/agentconfig/ -run TestLoadRepoAgents -v` (now 6 files under `agents/`).
3. `workflows/sdlc.yaml`, `sdlc-openai.yaml`; the demo directory.
4. `SkipExport` in `export.go` and its test.
5. `evals`: `gradeWorkspace` refactor (existing tests pass unchanged), `pipeline.go`, `stopWord`, `classifyWorkflow`; `pipeline_test.go`. See each new test fail once.
6. CLI: `eachAgent`, `eval_workflow.go`, `eval.go`; tests; `sdlc_test.go`.
7. `eval-live.yml`, `live-eval.md`, `README.md`, design doc, `interfaces.md`.
8. `make fmt && make ci`. Run the README demo by hand once (Docker) and paste its output into the PR's Testing section.

## Tests

### `internal/evals/pipeline_test.go`

Use `newRunnerEnv(t, nil)` (`runner_test.go`) and set `r.RunWorkflow`.
`fixingWorkflow(t)` returns a `RunWorkflow` that copies `wr.RepoDir` without
`.git` into `t.TempDir()/ws`, runs `fixCmd` there (`sh -c`, `cmd.Dir = ws`), and returns
`WorkflowResult{Outcome: "WORKFLOW_OUTCOME_COMPLETED", Workspace: ws, State: &eventlog.RunState{Step: 7, ToolCalls: 9, Usage: eventlog.Usage{USDMicros: 5000}}}`.
`counterID()` returns a `NewID` giving `flow-v1-1`, `flow-v1-2`, …; `pipe = Pipeline{Agent: "flow@1", Digest: "sha256:abc", NewID: counterID()}`.

| Test | Asserts |
|------|---------|
| `TestRunPipelineGradesFinalWorkspace` | one result: `Provider "workflow"`, `Agent "flow@1"`, `AgentDigest "sha256:abc"`, `RunID "flow-v1-1"`, `Attempt 1`, `StopReason "WORKFLOW_OUTCOME_COMPLETED"`, `Solved()`, `ChangedFiles` = the fix file, `Grade.Steps 7`, `Grade.ToolCalls 9`, `Grade.USDMicros 5000` |
| `TestRunPipelinePassesInputs` | the recorded `WorkflowRun`: `RepoDir` has a `.git` and `git log` shows one commit `base`; `os.ReadFile(IssuePath)` equals `bug.Issue`; `ID == RunID` of the result |
| `TestRunPipelineHiddenTestNotInRepo` | inside `RunWorkflow`, `HiddenTestPath(bug)` does not exist under `RepoDir` |
| `TestRunPipelineUnchangedIsUnsolved` | a `RunWorkflow` that copies without fixing → not solved, `FilesChanged 0`, no `Error` |
| `TestRunPipelineWorkflowError` | `RunWorkflow` returns `errors.New("boom")` → `Error == "run workflow: boom"`, `Grade == nil` |
| `TestRunPipelineRepeat` | `Repeat = 2` → two results, `Attempt` 1 and 2, `RunID` `flow-v1-1` and `flow-v1-2` |
| `TestRunPipelineNeedsRunWorkflow` | `RunWorkflow` nil → error, no result |
| `TestRunPipelineCancelled` | cancelled ctx → `ctx.Err()` returned, no result |
| `TestAnalyzeFailuresWorkflowResults` | an `EventReader` whose `Read` calls `t.Fatal`; three unsolved workflow results: `WORKFLOW_OUTCOME_BUDGET_EXHAUSTED` → `budget`, `workflow budget exhausted`; `WORKFLOW_OUTCOME_UNSPECIFIED` → `refused-or-error`; `WORKFLOW_OUTCOME_REVIEW_EXHAUSTED` with `Grade{FilesChanged: 1}` → `no-repro`, evidence starts with `review_exhausted; ` |
| `TestPrintTableWorkflowOutcome` | a report with one workflow result → its row has `workflow` and `COMPLETED` (not `WORKFLOW_OUTCOME_COMPLETED`) |

### `internal/workflow/export_test.go`

| Test | Asserts |
|------|---------|
| `TestSkipExportPassesWithoutBranch` | W.5's gate fixture (`newGateEnv`, the `flow` workflow) with `Gates: GateAutoApprove` and `SkipExport: true` → workflow `COMPLETED`; `task_finished{release, EXPORT, PASSED, "export skipped", commit ""}`; the repo has no `adlc/*` branch |

### `internal/cli/eval_workflow_test.go`

`writeEvalWorkflow(t, maxUSD string, agentPaths ...string) string` writes a
workflow `name: flow`, `version: 1`, with inputs `repo`/`issue`, one agent task per path (ids `t1`, `t2`, …,
prompt `go`), and `budget: {max_usd: <maxUSD>}` unless empty. Agents from
`writeBudgetAgent` (`eval_test.go`).

| Test | Asserts |
|------|---------|
| `TestEvalWorkflowDryRun` | `eval <miniSuite> --workflow <wf with max_usd 3.00> --repeat 2 --dry-run` → code 0, stdout exactly `bugs=1 workflow=flow@1 repeat=2 worst_case_usd=6.000000\n` |
| `TestEvalWorkflowDryRunNeedsMaxUSD` | no workflow budget → code 1, `has no budget.max_usd`, empty stdout |
| `TestEvalWorkflowAndAgentsExclusive` | both flags → code 1, `give either --agents or --workflow` |
| `TestEvalRequiresAgents` | (existing) still passes |
| `TestEvalWorkflowRejectsFakeAgent` | an agent with provider fake → code 1, `provider fake is not supported by adlc eval`, nothing about Docker in the output |
| `TestEvalWorkflowNeedsRepoAndIssue` | a workflow whose inputs are `repo` and `notes` → code 1, `must declare exactly the inputs repo (dir) and issue (file)` |
| `TestEvalWorkflowUnknownBug` | `--bugs nope --dry-run` → code 1, `unknown bug "nope"` |

`cli_test.go`: add `"eval": newEvalCmd()` to `TestCommandsShareDefaultControlDB`
and to W.5's `TestWorkflowCommandsShareDefaultWorkflowDB`.

### `internal/cli/sdlc_test.go`

`setModelEnv(t)` sets `ADLC_ANTHROPIC_MODEL`, `ADLC_OPENAI_MODEL` to `test-model`
and every `ADLC_*_PRICE_*` to `1.00` (as `TestLoadRepoAgents` does).

| Test | Asserts |
|------|---------|
| `TestRepoWorkflowsLoad` | for `../../workflows/sdlc.yaml`, `../../workflows/sdlc-openai.yaml`, `../../examples/sdlc-demo/workflow.yaml`: `workflow.Load` succeeds and `eachAgent` loads every agent (`len` = 3 per workflow); the two `workflows/` files pass `loadEvalWorkflow` and their dry run prints `worst_case_usd=30.000000` for the 10 go-bugs (`../../evals/suites/go-bugs`) |
| `TestSDLCWorkflowsMatch` | `sdlc.yaml` and `sdlc-openai.yaml` have equal tasks except the agent paths (`anthropic` ↔ `openai` in the file names) and different names |
| `TestDemoMatchesSDLCWorkflow` | for every task id, the demo's `Prompt`, `Checks`, `DependsOn`, `Kind` and `Gate` equal `sdlc.yaml`'s |
| `TestReviewerPromptHasVerdictLines` | `reviewer.prompt.md` contains the lines `VERDICT: APPROVE` and `VERDICT: CHANGES`; neither prompt names a tool other than the five READ tools and the two forbidden ones |
| `TestSDLCDemoRunsOffline` | skips without `git` or `go` on PATH. Copy `examples/sdlc-demo/repo` to a temp dir and `git init` + commit it; `workflowRun` with W.5's `hostSandbox(t)`, the three `--fake-script`s, dbs in `t.TempDir()` → code 0, status `awaiting_approval`, `tasks:     2/3 passed`; then `workflowApprove(…, "release", …)` with `by: "demo"` → code 0, `outcome:   completed`; `git -C <repo> show adlc/<id>:greet/greet.go` contains `name = "world"`; `adlc/<id>` has `greet/greet_issue_test.go`; the repo's `main` is unchanged |

`TestLoadRepoAgents` (existing) now loads the four new agent files; it must pass unchanged.

## Verify

```bash
go test ./internal/agentconfig/ -run TestLoadRepoAgents -v                    # PASS, 6 files
go test -race ./internal/evals/ -v -run 'Pipeline|WorkflowResults|WorkflowOutcome'   # all PASS
go test ./internal/evals/                                                     # existing tests unchanged, PASS
go test -race ./internal/workflow/ -run SkipExport -v                         # PASS
go test -race ./internal/cli/ -run 'EvalWorkflow|EvalRequires|RepoWorkflows|SDLC|Demo|Reviewer|CommandsShare' -v   # all PASS
grep -rn 'name:' agents/sdlc/*.yaml | grep -v -e '^[^:]*:[0-9]*:name:' -e 'ADLC_' || echo ok   # prints ok: every model name is a variable
make ci                                                                       # passes
```

```bash
# set ADLC_ANTHROPIC_MODEL and the ADLC_ANTHROPIC_PRICE_* variables first (any values for a dry run)
./bin/adlc eval evals/suites/go-bugs --workflow workflows/sdlc.yaml --dry-run
# bugs=10 workflow=sdlc@1 repeat=1 worst_case_usd=30.000000
```

The README demo, by hand with Docker: the first command ends with
`status:    awaiting_approval`; after `approve`, `git log` shows the branch commit
`adlc: workflow sdlc-demo-v1-…` (workflow ids have no `@`, m3.md decision 14).

## Out of scope

- A live run of the pipeline on the suite and its numbers (run `eval-live` with `workflow` set; recording results is a follow-up like task 1.10).
- Tuning the spec and reviewer prompts (an experiment loop like task 1.10, after a first live run).
- More SDLC roles from design §4 (triage, architect, tester) and parallel tasks (m3.md decision 5).
- Model-graded rubrics for spec or review quality (design §3.8 kind 3).
- Answering a child run's tool approval during an eval (it is graded as `workflow did not complete`).
- A digest over the workflow file and every agent file it names (see the `AgentDigest` decision).
- A `make` target for the demo.

## Pitfalls

- No model id and no price anywhere: `${ADLC_*_MODEL}` and `${ADLC_*_PRICE_*}` only; fake agents need neither.
- The fake scripts' `arguments` are JSON objects; Go source inside them escapes `"` as `\"`, newlines as `\n` and tabs as `\t`. Check with `go test` (the demo test fails on a broken script).
- Each fake script starts from its first turn in every child run (W.5 builds a new fake model per run); the demo passes every check the first time on purpose.
- `--fake-script` keys are agent names (`spec-fake`), not file names.
- `examples/sdlc-demo/repo` is a nested module (`go.mod`): do not add it to the main module, and keep its `go` directive old.
- The demo repository must be copied and `git init`ed before a run: the export refuses a directory that is not the top level of its own git repository.
- `RunPipeline` writes the hidden test only into `after`, after the workflow ended. Never pass a workspace prepared `WithHiddenTest` as `repo`.
- Pipeline results' `RunID` is a workflow id: never pass it to code that reads the run log (that is why `classifyWorkflow` exists).
- `--agents` lost `MarkFlagRequired`; the either/or check must run before anything is loaded.
- The engine's `BaseDir` for evals is `<eval workdir>/workflows` (m3.md "Pipeline evals"), not `.adlc/workflows`: workflow workspaces are removed with the eval workdir unless `--keep-workspaces` is given. The workflow log (`--workflow-db`) is kept either way, so `adlc workflow status` still lists the runs.

## Done checklist

- [ ] Spec and reviewer prompts and four agent files; read-only policies with explicit deny rules; `TestLoadRepoAgents` passes
- [ ] `workflows/sdlc.yaml`, `workflows/sdlc-openai.yaml` exactly as above
- [ ] Offline demo runs in `go test` (`TestSDLCDemoRunsOffline`) and by hand with Docker
- [ ] `Options.SkipExport` implemented (test)
- [ ] `Runner.RunPipeline`, `gradeWorkspace` refactor, `classifyWorkflow`, `stopWord`; existing evals tests unchanged
- [ ] `adlc eval --workflow` with dry run; `--agents` behaviour unchanged
- [ ] `eval-live.yml` input `workflow`; `live-eval.md`, `README.md`, design §4, `interfaces.md` updated
- [ ] No model id committed
- [ ] `make ci` passes
