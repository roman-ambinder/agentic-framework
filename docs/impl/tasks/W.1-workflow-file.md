# W.1 — Workflow file: schema, loader, validation

Size: M · Depends on: — · Branch: `task/W.1-workflow-file` · PR title: `W.1: workflow file schema, loader, validation and prompt rendering`

## Goal

After this task, package `internal/workflow` can read a workflow file
(`workflows/*.yaml`, m3.md "Workflow file") into a `*workflow.Workflow`,
reject every malformed file with an error that names the field
(`tasks[1].review.max_rounds: must be between 1 and 5, got 9`), give the
tasks in their fixed run order (`Order`, m3.md decision 5), make a new
workflow id (`ID`), and render a task's prompt template with the four
functions `input`, `file`, `result`, `diff` (m3.md decision 6), each value
capped at 64 KiB. Nothing runs yet: the engine (W.3) uses this package.
W.1 runs in parallel with W.2 (events and fold); neither needs the other's code.

## Read first

- `AGENTS.md`; `docs/impl/m3.md` (all of it; "Workflow file" and "Types" are the contract)
- `docs/impl/interfaces.md` §1, §7, §11 ("Control plane", `ParseRef`)
- `internal/agentconfig/agentconfig.go` (`Load`, `parse`: strict decoding with
  `yaml.UnmarshalWithOptions(data, &a, yaml.Strict())` and `yaml.FormatError(err, false, true)`)
- `internal/agentconfig/validate.go` (`problems`, `p.add(path, format, ...)`, `errors.Join`, `moneyMsg`, `validateBudget`)
- `internal/agentconfig/money.go` (`ParseMicroUSD`: you call it, you do not copy it)
- `internal/agentconfig/include.go` (how a cycle is printed: `a -> b -> a`)
- `internal/agentconfig/agentconfig_test.go` (`wantErr`, `TestValidateTable` style), `include_test.go` (`TestPolicyIncludeCycle`)
- `internal/controlplane/registry.go` (`refRE`, `ParseRef`): the registry reference syntax
- Go docs of `text/template` (`Funcs`, `Option("missingkey=error")`, `Execute`)

## Files

Create:

```
internal/workflow/doc.go              package comment (identical in W.1 and W.2, see below)
internal/workflow/workflow.go         types, constants, ErrInvalidWorkflow, Load, Parse, decoding, Order, ID, agentFile
internal/workflow/validate.go         problems, Validate and its rules, findCycle, dependency closure, prompt dry run
internal/workflow/render.go           Sources, MaxTemplateValueBytes, TruncatedMarker, Render, template functions, capValue
internal/workflow/workflow_test.go
internal/workflow/validate_test.go
internal/workflow/render_test.go
internal/workflow/refsync_test.go     package workflow_test: registry syntax matches controlplane.ParseRef
internal/workflow/testdata/sdlc.yaml  the example of m3.md, byte for byte the YAML block under "Workflow file"
```

Change:

```
docs/impl/interfaces.md   §1: one row; new §12 at the end (text below)
```

W.2 owns `internal/workflow/state.go`, `fold.go` and `fold_test.go`; do not
create files with those names. **`go.mod` and `go.sum` do not change**
(`github.com/goccy/go-yaml` and `internal/agentconfig` are already there).
No change to `events.proto`, `internal/eventlog`, `internal/cli`.

## Design

### `doc.go` (shared with W.2)

W.1 and W.2 both create this file with exactly this content, so whichever
merges second has an identical file and git merges it cleanly. If it is
already on `main` when you branch, leave it.

```go
// Package workflow loads workflow files, runs their tasks as child agent
// runs and folds workflow event logs into State (docs/impl/m3.md).
package workflow
```

### Types and constants (`workflow.go`)

The exported types are exactly those of m3.md "Types". Copy them with a doc
comment on every exported name. m3.md gives the string values of
`InputKind`, `TaskKind` and `GateKind` but no constant names; use these:

```go
// InputKind is the kind of a workflow input.
type InputKind string

// Input kinds.
const (
	InputDir  InputKind = "dir"  // a host directory; the engine copies it into the workspace
	InputFile InputKind = "file" // a host file
	InputText InputKind = "text" // a literal value
)

// TaskKind says what runs a task.
type TaskKind string

// Task kinds.
const (
	TaskAgent  TaskKind = "agent"  // an agent run (default)
	TaskExport TaskKind = "export" // no agent: the engine exports the workspace (task W.5)
)

// GateKind says whether a task waits for a person before it starts.
type GateKind string

// Gate kinds.
const (
	GateNone     GateKind = ""         // no gate ("none" in the file)
	GateApproval GateKind = "approval" // park the workflow until `adlc workflow approve`
)

// ErrInvalidWorkflow means a workflow file or Workflow value breaks a rule.
// Every error from Parse, Load (except a read error) and Validate wraps it.
var ErrInvalidWorkflow = errors.New("workflow: invalid workflow")
```

`Input`, `Budget`, `Review`, `Task` exactly as in m3.md. `Workflow` as in
m3.md plus one unexported field for tests:

```go
type Workflow struct {
	Name    string
	Version int
	Dir     string // directory of the file; agent paths resolve against it
	Inputs  []Input
	Budget  Budget
	Tasks   []Task // in file order
	Digest  string // "sha256:<hex>" of the file bytes

	// rand is the random source of ID; nil = crypto/rand. Tests set it.
	rand io.Reader
}
```

### Decoding (`workflow.go`, unexported)

```go
type fileWorkflow struct {
	Name    string      `yaml:"name"`
	Version int         `yaml:"version"`
	Inputs  []fileInput `yaml:"inputs"`
	Budget  fileBudget  `yaml:"budget"`
	Tasks   []fileTask  `yaml:"tasks"`
}

type fileInput struct {
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
}

type fileBudget struct {
	MaxUSD       usdText `yaml:"max_usd"`
	MaxTokens    int64   `yaml:"max_tokens"`
	MaxWallClock string  `yaml:"max_wall_clock"`
}

type fileTask struct {
	ID        string      `yaml:"id"`
	Kind      string      `yaml:"kind"`
	Agent     string      `yaml:"agent"`
	Prompt    string      `yaml:"prompt"`
	DependsOn []string    `yaml:"depends_on"`
	Checks    []string    `yaml:"checks"`
	Attempts  int         `yaml:"attempts"`
	Budget    fileBudget  `yaml:"budget"`
	Review    *fileReview `yaml:"review"`
	Gate      string      `yaml:"gate"`
}

type fileReview struct {
	Critic    string `yaml:"critic"`
	MaxRounds int    `yaml:"max_rounds"`
}
```

**`max_usd` is read as the literal text of the YAML scalar.** m3.md writes
`max_usd: 2.00` unquoted (agent files quote it: `"1.00"`). Decoding an
unquoted number into a `string` with goccy/go-yaml goes through a float and
loses the text: `0.000001` becomes `"1e-06"` and `010` becomes `"8"`. So
`usdText` implements `yaml.NodeUnmarshaler` and keeps the token as written:

```go
// usdText is a max_usd value exactly as written in the file, quoted or not.
type usdText string

// UnmarshalYAML keeps the scalar's source text (goccy/go-yaml NodeUnmarshaler).
func (u *usdText) UnmarshalYAML(n ast.Node) error {
	switch n.Type() {
	case ast.StringType, ast.IntegerType, ast.FloatType:
		*u = usdText(n.GetToken().Value)
		return nil
	case ast.NullType:
		*u = ""
		return nil
	}
	return fmt.Errorf("max_usd must be a number or a string, got %s", n.Type())
}
```

(`ast` is `github.com/goccy/go-yaml/ast`, same module; this was tried with
v1.19.2 and `yaml.Strict()`: `2.00` → `"2.00"`, `0.000001` → `"0.000001"`,
`"1.50"` → `"1.50"`, `[1]` → the error above.)

### `Parse` and `Load`

```go
// Load reads the file at path and returns Parse's result with Dir set to the
// file's absolute directory.
func Load(path string) (*Workflow, error)

// Parse decodes data strictly, applies defaults, converts budgets and
// validates. dir is stored in Workflow.Dir as given.
func Parse(data []byte, dir string) (*Workflow, error)
```

`Load`, numbered:

1. `data, err := os.ReadFile(path)`; error → `fmt.Errorf("workflow: read %s: %w", path, err)` (not `ErrInvalidWorkflow`).
2. `abs, err := filepath.Abs(path)`; error → `fmt.Errorf("workflow: %s: %w", path, err)`.
3. `w, err := Parse(data, filepath.Dir(abs))`; error → `fmt.Errorf("%s: %w", path, err)`
   (the inner error already starts with `workflow: invalid workflow:`).

`Parse`, numbered:

1. `var f fileWorkflow`; `yaml.UnmarshalWithOptions(data, &f, yaml.Strict())`; error →
   `fmt.Errorf("%w: decode: %s", ErrInvalidWorkflow, yaml.FormatError(err, false, true))`.
   Unknown keys (`unknown field "max_tokenz"`), duplicate keys and wrong YAML types fail here.
2. `w := &Workflow{Name: f.Name, Version: f.Version, Dir: dir, Digest: digestOf(data)}` with
   `digestOf(b) = "sha256:" + hex.EncodeToString(sha256.Sum256(b)[:])` (of exactly the bytes given, comments included).
3. Inputs: `Input{Name, InputKind(Kind)}` in file order (no default for `kind`).
4. `var p problems`; `w.Budget = convertBudget(&p, "budget", f.Budget)`.
5. Tasks, in file order, index `i`, `path := fmt.Sprintf("tasks[%d]", i)`:
   `Kind`: `""` → `TaskAgent`, else `TaskKind(raw)`;
   `Attempts`: `0` → `1`, else as written (negative stays negative and fails validation);
   `Gate`: `""` and `"none"` → `GateNone`, else `GateKind(raw)`;
   `Budget = convertBudget(&p, path+".budget", ft.Budget)`;
   `Review`: nil stays nil, else `&Review{Critic, MaxRounds}` (no default for `max_rounds`);
   `ID`, `Agent`, `Prompt`, `DependsOn`, `Checks` copied as written.
6. `p = append(p, w.check()...)` (the rules below); `if err := p.err(); err != nil { return nil, err }`.
7. Return `w`.

`convertBudget(p *problems, path string, b fileBudget) Budget`:

| Field | Conversion | Problem (path, message) |
|-------|-----------|-------------------------|
| `max_usd` | `agentconfig.ParseMicroUSD(string(b.MaxUSD))` (`""` → 0) | `<path>.max_usd`, `%q is not a decimal amount with at most 6 decimals` (the value as written) |
| `max_tokens` | as is | none here (Validate checks `>= 0`) |
| `max_wall_clock` | `""` → 0; else `time.ParseDuration`, must be `> 0` | `<path>.max_wall_clock`, `%q is not a positive duration` |

On a problem the field stays 0.

### Validation (`validate.go`)

```go
type problems []error

func (p *problems) add(path, format string, args ...any) // like agentconfig: "<path>: <message>"

// err returns nil for no problems, else ErrInvalidWorkflow wrapping all of them.
func (p problems) err() error {
	if len(p) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidWorkflow, errors.Join(p...))
}

// Validate checks every rule and returns all problems at once, wrapped in
// ErrInvalidWorkflow; each problem's text is "<path>: <message>".
func (w *Workflow) Validate() error { return w.check().err() }

// check returns the problems of w in the order of the table below.
func (w *Workflow) check() problems
```

So an error reads
`workflow: invalid workflow: name: must match ...\ntasks[0].id: ...`.
Validate never touches the file system or the network.

Rules, checked in this order (`%q`/`%d` are the offending value; `i` is
the task index, `j` the index inside a list):

| # | Path | Rule | Message |
|---|------|------|---------|
| 1 | `name` | matches `^[a-z0-9][a-z0-9-]*$` and `len <= 64` | `must match ^[a-z0-9][a-z0-9-]*$ and be at most 64 characters, got %q` |
| 2 | `version` | `1 <= v <= 999999999` (it is a `uint32` in `workflow_started`) | `must be between 1 and 999999999, got %d` |
| 3 | `inputs[j].name` | matches `^[a-z][a-z0-9_]*$` and `len <= 32` | `must match ^[a-z][a-z0-9_]*$ and be at most 32 characters, got %q` |
| 4 | `inputs[j].name` | not used by an earlier input (only when rule 3 passed) | `duplicate input %q` |
| 5 | `inputs[j].kind` | `dir`, `file` or `text` | `must be one of dir, file, text, got %q` |
| 6 | `budget` | `checkBudget(p, "budget", w.Budget)` (below) | |
| 7 | `tasks` | at least one task | `at least one task is required` |
| 8 | `tasks[i].id` | same pattern and length as `name` | `must match ^[a-z0-9][a-z0-9-]*$ and be at most 64 characters, got %q` |
| 9 | `tasks[i].id` | not used by an earlier task (only when rule 8 passed) | `duplicate task id %q` |
| 10 | `tasks[i].kind` | `agent` or `export` | `must be one of agent, export, got %q` |
| 11 | `tasks[i].agent` | kind agent: not empty | `is required` |
| 12 | `tasks[i].agent` | kind agent, not empty: `checkAgentRef` (below) | `%q is neither an agent file (.yaml or .yml) nor NAME@N, NAME@staging or NAME@prod` |
| 13 | `tasks[i].prompt` | kind agent: `strings.TrimSpace(prompt) != ""` | `is required` |
| 14 | `tasks[i].checks[j]` | kind agent: `strings.TrimSpace(check) != ""` | `is empty` |
| 15 | `tasks[i].review.critic` | kind agent, review set: rules 11 and 12 with this path | `is required` / as rule 12 |
| 16 | `tasks[i].review.max_rounds` | kind agent, review set: `1..5` | `must be between 1 and 5, got %d` |
| 17 | `tasks[i].agent`, `.prompt`, `.checks`, `.review`, `.budget` | kind export: agent `""`, prompt `""`, no checks, review nil, budget zero | `not allowed for kind export` (one problem per field that is set) |
| 18 | `tasks[i].attempts` | kind agent: `1..5`; kind export: exactly 1 | `must be between 1 and 5, got %d` / `must be 1 for kind export, got %d` |
| 19 | `tasks[i].budget` | kind agent: `checkBudget(p, path+".budget", t.Budget)` | |
| 20 | `tasks[i].gate` | `""` or `approval` | `must be one of none, approval, got %q` |
| 21 | `tasks[i].depends_on[j]` | not `""` and names a task in the file | `unknown task %q` |
| 22 | `tasks[i].depends_on[j]` | not the task's own id | `task %q depends on itself` |
| 23 | `tasks[i].depends_on[j]` | not listed earlier in the same list | `duplicate dependency %q` |
| 24 | `tasks` | no cycle: `findCycle` (below) | `dependency cycle: a -> b -> c -> a` |
| 25 | `tasks[i].prompt` | kind agent, prompt not blank: the dry run (below) succeeds | the template error, `%v` |

Rules 10–18 that depend on the kind are skipped when rule 10 failed. Rule 17
checks `t.Budget != (Budget{})`; rule 19 is not run for export tasks.

`checkBudget(p *problems, path string, b Budget)` (catches values a caller
built in code; Parse cannot produce them except `max_tokens`):

| Path | Rule | Message |
|------|------|---------|
| `<path>.max_usd` | `MaxUSDMicros >= 0` | `must be >= 0, got %d` |
| `<path>.max_tokens` | `MaxTokens >= 0` | `must be >= 0, got %d` |
| `<path>.max_wall_clock` | `MaxWallClock >= 0` | `must be >= 0, got %s` |

A task budget larger than the workflow budget is not an error (nesting takes
the smaller value, W.3).

### Agent references

`Task.Agent` and `Review.Critic` keep the string as written. W.1 checks its
syntax only; **it does not check that the file exists or that the registry
has the version**:

- Parse works on bytes and Validate must stay pure (tests, and W.3 re-parses
  the file on resume to compare digests).
- A registry reference needs the control database, which W.1 does not open.
- W.3 resolves and loads every agent of the workflow before it writes
  `workflow_started`, so a missing file still fails before any work is done.

```go
// refRE is the syntax of controlplane.ParseRef (NAME@N, NAME@staging,
// NAME@prod). It is copied, not imported, to keep internal/workflow free of
// internal/controlplane (which imports internal/evals); refsync_test.go
// keeps the two in step.
var refRE = regexp.MustCompile(`^([a-z][a-z0-9-]{0,31})@([1-9][0-9]{0,8}|staging|prod)$`)

// checkAgentRef adds a problem at path unless ref is a registry reference or
// a path whose extension is .yaml or .yml. The caller checks ref != "".
func checkAgentRef(p *problems, path, ref string)

// agentFile says how an agent reference names its agent: a registry
// reference (registry true, path "") or a file (path made absolute against
// w.Dir when it is relative; filepath.Clean applied). W.3 calls it.
func (w *Workflow) agentFile(ref string) (path string, registry bool)
```

A reference can never be both: `refRE` allows no dot, and a path needs a
`.yaml`/`.yml` extension.

### Cycles and dependency closure

```go
// findCycle returns the first dependency cycle, searching tasks in file
// order and each task's depends_on in list order, as ids with the first id
// repeated at the end (a -> b -> a); nil if there is none. Unknown ids and
// self-dependencies are skipped: rules 21 and 22 report them.
func findCycle(tasks []Task) []string
```

Algorithm: `index` maps each id to its first task index. Depth-first search
with `state[i]` 0 (new), 1 (on the stack), 2 (done) and a `stack []string`
of ids. Visiting `i`: state 1, push its id; for each dependency `d` with
`j := index[d]` (skip unknown and `j == i`): state 1 → return
`append(slices.Clone(stack[k:]), d)` where `k = slices.Index(stack, d)`;
state 0 → recurse and return a non-nil result; afterwards pop, state 2.
Message: `p.add("tasks", "dependency cycle: %s", strings.Join(cycle, " -> "))`.
For tasks `a`(depends on `b`), `b`(on `c`), `c`(on `a`) it is
`tasks: dependency cycle: a -> b -> c -> a`.

```go
// dependencies returns every task task i depends on, directly or through
// other tasks (ids; unknown ids skipped). Safe with cycles.
func dependencies(tasks []Task, i int) map[string]bool
```

### `Order`

```go
// Order returns the tasks in run order: a task comes after everything it
// depends on, and among tasks that are ready the one first in the file goes
// first. Call it on a valid workflow; with a cycle, the tasks that cannot be
// ordered are appended in file order, so every task is returned once.
func (w *Workflow) Order() []Task
```

Repeat `len(w.Tasks)` times: pick the first task (lowest index) not yet
emitted whose known dependencies are all emitted; if there is none, append
every remaining task in file order and stop. O(n²) is fine. Returns a new
slice (the `Task` values share their slices with `w.Tasks`; callers must not
modify them).

### `ID`

```go
// ID returns a new workflow id "<name>@<version>-<UTC yyyymmddThhmmssZ>-<6 hex>",
// e.g. "sdlc@1-20261114T080507Z-abcdef". The 6 hex digits are 3 random bytes.
func (w *Workflow) ID(now time.Time) string
```

`var b [3]byte`; if `w.rand != nil` then `_, _ = io.ReadFull(w.rand, b[:])`
(a short test reader leaves zeros), else `_, _ = rand.Read(b[:])`
(`crypto/rand`; it never returns an error). Then
`fmt.Sprintf("%s@%d-%s-%s", w.Name, w.Version, now.UTC().Format("20060102T150405Z"), hex.EncodeToString(b[:]))`.
`ID` never calls `time.Now`.

### Rendering (`render.go`)

```go
// Sources supplies the values of the prompt functions (the engine, W.3).
type Sources interface {
	Input(name string) (string, error)    // {{ input "name" }}
	File(path string) (string, error)     // {{ file "path" }}, path relative to the workspace
	Result(taskID string) (string, error) // {{ result "task" }}: an earlier task's final message
	Diff(taskID string) (string, error)   // {{ diff "task" }}: the change an earlier task committed
}

// MaxTemplateValueBytes caps every value a prompt function returns.
const MaxTemplateValueBytes = 64 << 10

// TruncatedMarker ends a value that was cut to MaxTemplateValueBytes.
const TruncatedMarker = "\n[truncated by adlc]\n"

// Render executes t.Prompt with the four functions backed by src. Errors
// name the task. A prompt without function calls needs no src (nil is allowed).
func Render(t Task, src Sources) (string, error)
```

Unexported pieces:

```go
// funcs returns the template functions backed by src; nil src → noSources{}.
func funcs(src Sources) template.FuncMap

// parsePrompt parses t.Prompt as template t.ID with funcs(src) and
// Option("missingkey=error"). Functions must be added before Parse.
func parsePrompt(t Task, src Sources) (*template.Template, error)

// capValue returns s unchanged if len(s) <= MaxTemplateValueBytes; otherwise
// a prefix of s followed by TruncatedMarker, MaxTemplateValueBytes bytes at
// most in total, never cutting a UTF-8 sequence.
func capValue(s string) string

// noSources answers every call with errNoSources.
type noSources struct{}

var errNoSources = errors.New("no value sources")
```

The four functions, each `func(string) (string, error)`:

| Function | Before calling src | Calls | Error text (wraps src's error with `%w`) | Result |
|----------|-------------------|-------|------------------------------------------|--------|
| `input` | — | `src.Input(name)` | `input %q: %w` | `capValue(v)` |
| `file` | `filepath.IsLocal(filepath.FromSlash(p))` else error `file %q: not a relative path inside the workspace` (src not called) | `src.File(p)` | `file %q: %w` | `capValue(v)` |
| `result` | — | `src.Result(id)` | `result %q: %w` | `capValue(v)` |
| `diff` | — | `src.Diff(id)` | `diff %q: %w` | `capValue(v)` |

`capValue`, exactly: `n := MaxTemplateValueBytes - len(TruncatedMarker)`;
while `n > 0 && !utf8.RuneStart(s[n])` do `n--`; return `s[:n] + TruncatedMarker`.

`Render`, numbered:

1. `tmpl, err := parsePrompt(t, src)`; error → `fmt.Errorf("workflow: task %q: parse prompt: %w", t.ID, err)`.
2. `var b strings.Builder`; `tmpl.Execute(&b, nil)`; error → `fmt.Errorf("workflow: task %q: render prompt: %w", t.ID, err)`.
   `text/template` wraps a function's error with `%w`, so `errors.Is(err, srcErr)` holds.
3. Return `b.String()`. An export task (prompt `""`) renders to `""`.

### Prompt dry run (rule 25, `validate.go`)

Validate runs every agent prompt once against a checking source, so a broken
template fails at load time, not in the middle of a workflow:

```go
// checkSources answers the dry run: declared inputs, any file, and only
// tasks the prompt's task depends on (directly or not).
type checkSources struct {
	inputs map[string]bool // declared input names
	deps   map[string]bool // dependencies(w.Tasks, i)
}
```

| Method | Returns |
|--------|---------|
| `Input(name)` | `"", nil` if declared, else `"", errors.New("not declared in inputs")` |
| `File(path)` | `"", nil` (the path check is in the `file` function itself) |
| `Result(id)`, `Diff(id)` | `"", nil` if `deps[id]`, else `"", errors.New("not a dependency of this task (list it in depends_on)")` |

For task `i`: `tmpl, err := parsePrompt(t, checkSources{...})`, then
`tmpl.Execute(io.Discard, nil)`; the first error of either →
`p.add(fmt.Sprintf("tasks[%d].prompt", i), "%v", err)`. This finds syntax
errors (`unclosed action`), unknown functions (`function "nope" not defined`),
field references (`{{ .Foo }}` → `nil data; no entry for key "Foo"`), wrong
arguments (`{{ input 1 }}` → `expected string; found 1`), undeclared inputs,
non-local file paths and `result`/`diff` of a task that is not a dependency.
Calls inside a branch that is not taken (`{{ if false }}...{{ end }}`) are
not checked; Render reports those when they run.

### `interfaces.md` text

§1, add a row after `internal/controlplane`:
`| internal/workflow | W.1–W.6 | workflow files, prompt rendering, workflow event fold, engine (docs/impl/m3.md) |`

Append at the end of the file:

```markdown
## §12 M3 additions (briefs W.1–W.6)

Shared decisions and the exact API: `docs/impl/m3.md` ("The API"). Constant
names m3.md leaves open, fixed by the briefs:

- **W.1:** `InputDir`, `InputFile`, `InputText`; `TaskAgent`, `TaskExport`;
  `GateNone`, `GateApproval`. Agent references are checked for syntax only
  (a `.yaml`/`.yml` path or `NAME@N`/`NAME@ENV`); the engine loads them.
- **W.2:** `StatusRunning`, `StatusAwaitingApproval`, `StatusCompleted`;
  `eventlog.WithFirstKind`; event payloads 23–28 (§10).
```

## Steps

1. Branch from the latest `main` (AGENTS.md).
2. `doc.go` exactly as above (skip if it is on `main` already).
3. `workflow.go`: types, constants, `ErrInvalidWorkflow`, the decode structs, `usdText`, `digestOf`, `convertBudget`, `Parse`, `Load`.
   Temporarily let `check` return nil. Write `TestParseFullExample` and `testdata/sdlc.yaml`; pass.
4. `validate.go`: `problems`, `check`, `Validate`, rules 1–24 in table order, `checkBudget`, `checkAgentRef`, `findCycle`, `dependencies`.
   Write `TestValidateTable`, `TestValidateReportsAllProblems`, `TestValidateCycle`, `TestValidateCodeBuiltWorkflow`; pass.
5. `render.go`: `Sources`, constants, `funcs`, `noSources`, `parsePrompt`, `capValue`, `Render`. Write `render_test.go`; pass.
6. Rule 25 (dry run) in `validate.go`; write `TestValidatePrompt`; pass.
7. `Order`, `ID`, `agentFile`; their tests; `refsync_test.go`.
8. `interfaces.md` §1 and §12.
9. Break each piece once on purpose (for example drop the `utf8.RuneStart` loop,
   or pick the last ready task in `Order`), see a test fail, restore.
10. `make fmt && make ci`.

## Tests

Package `workflow` unless noted. Test helpers (the only package-level
identifiers in W.1's test files besides `Test...` functions, so that W.2's
test files never collide with them): `minimalYAML` (const: `"name: wf\nversion: 1\ntasks:\n  - id: a\n    agent: a.yaml\n    prompt: do it\n"`),
`mustParse(t, yaml string) *Workflow`, `parseErr(t, yaml string, subs ...string)` (asserts
`errors.Is(err, ErrInvalidWorkflow)` and that the text contains every `subs`),
`fakeSources` (maps `inputs`, `files`, `results`, `diffs`; `err error` returned by every call when set;
`calls []string` recording `"input:repo"`, `"file:spec.md"`, ...).

`workflow_test.go`:

| Test | Asserts |
|------|---------|
| `TestParseFullExample` | `Parse(testdata/sdlc.yaml bytes, "/wf")` equals (`reflect.DeepEqual`) `Workflow{Name: "sdlc", Version: 1, Dir: "/wf", Inputs: [{repo dir} {issue file}], Budget: {2_000_000, 2_000_000, time.Hour}, Digest: "sha256:"+hex(sha256(bytes))}` with tasks: `spec` {Kind agent, Agent `../agents/sdlc/spec.yaml`, Prompt as in the file, Attempts 1, Gate ""}; `implement` {DependsOn [spec], Checks [`go build ./...`, `go test ./...`], Attempts 2, Budget {MaxUSDMicros: 800_000}, Review {`../agents/sdlc/reviewer.yaml`, 2}}; `release` {Kind export, DependsOn [implement], Gate approval, Attempts 1, Agent "", Prompt ""} |
| `TestParseDefaults` | `minimalYAML` → task Kind `agent`, Attempts 1, Gate `""`, Review nil, Budget zero; with `gate: none` → Gate `""`; with `attempts: 0` → 1 |
| `TestParseUnknownField` | `budget: {max_tokenz: 1}` → `parseErr(..., "decode:", "unknown field \"max_tokenz\"")`; a duplicate key `name` twice → contains `already defined` |
| `TestParseMaxUSD` | table, as `budget: {max_usd: V}`: `2.00`→2_000_000, `0.80`→800_000, `"1.5"`→1_500_000, `0.000001`→1, `2`→2_000_000, `0`→0; errors with message `budget.max_usd: "V" is not a decimal amount with at most 6 decimals` for `1e3`, `-1`, `010`, `0.1234567`, `"x"`; `[1]` → contains `max_usd must be a number or a string` |
| `TestParseTaskBudget` | `max_usd` on task 0 `"x"` → `tasks[0].budget.max_usd: "x" is not a decimal amount`; `max_wall_clock: 90m` → 90 minutes |
| `TestParseWallClock` | `1h30m` → 90 min; `soon`, `0s`, `-1m` → `budget.max_wall_clock: "<v>" is not a positive duration` |
| `TestDigestIsOfFileBytes` | two inputs that differ only in a comment → different digests; each equals `"sha256:"+hex(sha256(data))` |
| `TestLoadSetsAbsoluteDir` | file `wf/x.yaml` in `t.TempDir()`; `t.Chdir(tmp)`; `Load("wf/x.yaml")` → `Dir == filepath.Join(tmp, "wf")` (absolute), Digest of the file bytes |
| `TestLoadMissingFile` | `errors.Is(err, fs.ErrNotExist)`, text starts with `workflow: read `, not `ErrInvalidWorkflow` |
| `TestLoadInvalidFileNamesPath` | invalid file → text starts with the path given, `errors.Is(err, ErrInvalidWorkflow)` |
| `TestOrder` | table (ids, `name(deps)`): `a b c` → `a b c`; `a b(c) c` → `a c b`; `d(b,c) b(a) c(a) a` → `a b c d`; `x y(x) z` → `x y z`; `b(a) a` → `a b` |
| `TestOrderWithCycleKeepsEveryTask` | Workflow literal `a(b) b(a) c` → `c a b` |
| `TestID` | `w.rand = bytes.NewReader([]byte{0xab, 0xcd, 0xef})`, now = 2026-11-14 09:05:07 in `time.FixedZone("x", 3600)` → `sdlc@1-20261114T080507Z-abcdef`; with `rand` nil two ids match `^sdlc@1-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{6}$` |
| `TestAgentFile` | `Dir: "/wf"`: `../agents/a.yaml` → `/agents/a.yaml`, false; `/abs/a.yml` → `/abs/a.yml`, false; `coder@1` and `coder@prod` → `""`, true |

`validate_test.go`:

| Test | Asserts |
|------|---------|
| `TestValidateTable` | one row per rule 1–23 (both messages of rules 15, 17, 18): a YAML built from `minimalYAML` with one change, and the exact `"<path>: <message>"` text expected; e.g. `name: Bad` → `name: must match ^[a-z0-9][a-z0-9-]*$ and be at most 64 characters, got "Bad"`; a 65-character name; `version: 0`; input `Repo`; two inputs `repo`; input kind `url`; budget `max_tokens: -1` → `budget.max_tokens: must be >= 0, got -1`; `tasks: []`; id `-a`; two tasks `a`; kind `script`; agent missing; agent `coder` and `coder@0` → rule 12; prompt `"  "`; check `""`; review without critic; `max_rounds: 0` and `6`; export with agent, prompt, checks, review, budget (5 rows); `attempts: 6`, `attempts: -1`; export `attempts: 2` → `tasks[1].attempts: must be 1 for kind export, got 2`; `gate: always`; `depends_on: [nope]`, `[a]` on task `a`, `[b, b]` |
| `TestValidateReportsAllProblems` | `name: X`, `version: 0`, task id `""` → one error containing all three messages, each on its own line |
| `TestValidateCycle` | `a(b) b(c) c(a)` → contains `tasks: dependency cycle: a -> b -> c -> a`; `a(a)` alone → contains `task "a" depends on itself` and not `dependency cycle` |
| `TestValidatePrompt` | table, prompt of task `b` (depends on `a`, inputs `issue`): `{{ if` → `tasks[1].prompt: template: b:1: unclosed action`; `{{ nope "x" }}` → `function "nope" not defined`; `{{ .Foo }}` → `nil data; no entry for key "Foo"`; `{{ input 1 }}` → `expected string; found 1`; `{{ input "nope" }}` → `input "nope": not declared in inputs`; `{{ result "c" }}` (c exists, not a dependency) → `result "c": not a dependency of this task (list it in depends_on)`; `{{ diff "c" }}` → same for diff; `{{ file "/etc/passwd" }}` and `{{ file "../x" }}` → `not a relative path inside the workspace`; valid: `{{ input "issue" }} {{ file "spec.md" }} {{ result "a" }} {{ diff "a" }}`; valid: task `c(b)` using `{{ result "a" }}` (transitive); valid: `{{ if false }}{{ input "nope" }}{{ end }}` |
| `TestValidateCodeBuiltWorkflow` | table of valid `Workflow` literals with one change each: task Kind `""` → `tasks[0].kind: must be one of agent, export, got ""`; `Budget{MaxUSDMicros: -1}` → `budget.max_usd: must be >= 0, got -1`; `Attempts: 0` → `tasks[0].attempts: must be between 1 and 5, got 0`; a valid literal → nil |

`render_test.go`:

| Test | Asserts |
|------|---------|
| `TestRenderFunctions` | prompt `I={{ input "issue" }} F={{ file "spec.md" }} R={{ result "spec" }} D={{ diff "spec" }}` → `I=i F=f R=r D=d`; `calls == [input:issue file:spec.md result:spec diff:spec]` |
| `TestRenderCapsEveryValue` | each function returns `strings.Repeat("x", 70000)` → each of the four parts has length `MaxTemplateValueBytes` and ends with `TruncatedMarker` |
| `TestCapValue` | exactly `MaxTemplateValueBytes` bytes → unchanged; one byte more → length `MaxTemplateValueBytes`, prefix `s[:MaxTemplateValueBytes-len(TruncatedMarker)]`; `strings.Repeat("é", 40000)` → `utf8.ValidString`, ends with the marker, length ≤ `MaxTemplateValueBytes` |
| `TestRenderErrorNamesTask` | `fakeSources{err: errBoom}`, task `spec` with `{{ input "issue" }}` → `errors.Is(err, errBoom)`; text contains `workflow: task "spec": render prompt:` and `input "issue": boom` |
| `TestRenderFileRejectsNonLocalPath` | `{{ file (input "p") }}` with input `p` = `../x` → error contains `file "../x": not a relative path inside the workspace`; `calls` has no `file:` entry |
| `TestRenderBadTemplate` | `Task{ID: "x", Prompt: "{{"}` → contains `workflow: task "x": parse prompt:` |
| `TestRenderNilSources` | `{{ input "a" }}` with nil src → error contains `no value sources`, no panic; `plain text` with nil src → `plain text` |
| `TestRenderExportTask` | `Task{ID: "release", Kind: TaskExport}` → `""`, nil |

`refsync_test.go` (package `workflow_test`, imports `internal/controlplane`):

| Test | Asserts |
|------|---------|
| `TestRegistryRefSyntaxMatchesControlplane` | for each of `hello@1`, `hello@staging`, `hello@prod`, `hello`, `hello@0`, `Hello@1`, `hello@dev`, `@1`, a 33-letter name `@1`, `hello@1234567890`: `workflow.Parse` of `minimalYAML` with `agent: <s>` succeeds exactly when `controlplane.ParseRef(s)` succeeds |

## Verify

```bash
go test ./internal/workflow/ -v -run 'Parse|Load|Validate|Order|ID|AgentFile|Render|CapValue|Registry'   # all PASS
go vet ./internal/workflow/
git diff --stat origin/main -- go.mod go.sum                                                            # empty
make ci                                                                                                 # passes
```

## Out of scope

- Loading agent files, checking they exist, resolving registry references (W.3).
- Converting `Budget` to `*adlcv1.Budget` and nesting budgets (W.3).
- Any event, the fold, `eventlog` (W.2).
- Checking that an export task has a `repo` input of kind `dir` (`ExportInput` is W.5's; W.5 adds that rule).
- Running checks, the review loop, gates, export, the CLI, `workflows/sdlc.yaml` (W.3–W.6).
- Parallel tasks (m3.md decision 5).

## Pitfalls

- Do not decode `max_usd` into a plain `string`: unquoted numbers lose their text (see `usdText`).
  goccy/go-yaml also turns `version: 1.5` and `max_tokens: 1.5` into 1 without an error; leave that, do not add custom decoders for them.
- Do not import `internal/controlplane` from non-test code (it imports `internal/evals`; a later task may make `evals` or the CLI depend on `workflow`). Copy `refRE`; `refsync_test.go` is an external test package, which may import it.
- `text/template`, not `html/template` (no HTML escaping in prompts).
  Add the functions with `Funcs` **before** `Parse`, or parsing fails with `function "input" not defined`.
- `capValue` counts bytes, not runes, and the marker is inside the 64 KiB, not after it.
- `ID` takes `now` from the caller; never call `time.Now` in this package's tested code. Tests never read `crypto/rand` output.
- Validate must not read files: tests call `Parse` with a made-up `dir`.
- Keep rule order: tests that check "all problems" compare one error's lines.
- Test identifiers: W.2 adds `fold_test.go` to the same package. Use only the helper names listed under Tests.
- Do not create `state.go` or `fold.go`, and do not define `Status`, `State`, `TaskState`, `Fold` or `ErrInvalidLog` (W.2).
- `go.mod` must not change. If `go mod tidy` wants to change it, stop and report.

## Done checklist

- [ ] `doc.go` identical to the text above
- [ ] `Parse`/`Load` decode strictly, apply defaults (kind agent, attempts 1, gate none), convert budgets, keep `max_usd` text exact
- [ ] Every rule 1–25 with its exact message; all problems reported at once, wrapped in `ErrInvalidWorkflow`
- [ ] Cycle message shows the path; self-dependency reported separately
- [ ] Agent references: syntax only, `agentFile` for W.3
- [ ] `Order` topological with ties in file order; `ID` format with injectable random source
- [ ] `Render` with four functions, 64 KiB cap with `TruncatedMarker`, errors name the task and wrap the source error
- [ ] `interfaces.md` §1 row and §12
- [ ] Each new test seen failing once
- [ ] `go.mod`/`go.sum` unchanged; `make ci` passes
