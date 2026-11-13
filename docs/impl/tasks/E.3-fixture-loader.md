# E.3 — Fixture format and loader

Size: S · Depends on: E.2 · Branch: `task/E.3-fixture-loader` · PR title: `E.3: fixture format, suite loader and seeded-bug self-check`

## Goal

After this task, `internal/evals` can load an eval suite from disk
(`LoadSuite`), validate every seeded bug, and prepare a workspace for a bug
(clean fixture + bug patch, as its own git repository). A self-check test,
run in its own CI job, proves for every bug that its hidden tests **fail** on
the buggy code and **pass** after the reference fix. Later tasks (E.4, 1.8)
reuse `PrepareWorkspace` and `ParseTestOutput`.

## Read first

- `AGENTS.md`
- `docs/impl/interfaces.md` §1 (package `internal/evals`), §9
- `docs/impl/tasks/E.1-fixture-repo.md` and `docs/impl/tasks/E.2-seeded-bugs.md`
- The directories `evals/fixtures/` and `evals/suites/go-bugs/` created by E.1 and E.2
- `Makefile`, `.github/workflows/ci.yml`

## Files

Create:
- `internal/evals/suite.go` — types, `LoadSuite`, validation
- `internal/evals/workspace.go` — `PrepareWorkspace`, `HiddenTestPath`, `copyDir`, `gitApply`, `gitCommitBase`
- `internal/evals/testoutput.go` — `TestOutcome`, `ParseTestOutput`, `HiddenTestArgs`
- `internal/evals/suite_test.go` — unit tests for loading and validation
- `internal/evals/testoutput_test.go` — unit tests for output parsing
- `internal/evals/selfcheck_test.go` — the slow self-check, build tag `evals`
- `internal/evals/testdata/suites/mini/ok-001/…` and `internal/evals/testdata/fixtures/tiny/…` — a tiny suite for unit tests (see Steps)

Change:
- `Makefile` — add target `evals-check`
- `.github/workflows/ci.yml` — add job `evals-check`

## Design

### On-disk format (the contract E.2 follows)

```
evals/
  fixtures/<fixture>/          a Go module (go.mod at its root), stdlib only
  suites/go-bugs/<bug-id>/
    meta.yaml                  see below
    issue.md                   the issue text a user would write; the agent sees only this
    bug.patch                  `git diff` output that introduces the bug into the clean fixture
    fix.patch                  `git diff` output that fixes it, applied after bug.patch
    hidden_test.go             test file the agent never sees
```

Patches are made with `git diff` inside the fixture directory, so their paths
look like `a/internal/stock/stock.go` / `b/internal/stock/stock.go` and are
relative to the fixture root.

`meta.yaml` (canonical format; task E.2 writes these files, and
`evals/scripts/check-bug.sh` from E.2 and the E.4 procedure read the
`fixture:` and `test_package:` lines and the `hidden_tests` block list with
`sed`, so keep top-level keys unindented and write lists as block lists):

```yaml
id: money-split-remainder
difficulty: easy            # easy | medium | hard
title: Splitting an amount loses a cent   # short human summary; the agent never sees it
fixture: ledger             # directory name under evals/fixtures
test_package: money         # where hidden_test.go goes, relative to the fixture root ("." = root)
hidden_tests:               # top-level test function names in hidden_test.go; each starts with TestHidden
  - TestHiddenSplitKeepsRemainder
timeout: 2m                 # max time for one `go test` run of the hidden tests
fix_files:                  # files the reference fix changes, relative to the fixture root
  - money/money.go
```

The hidden test is always copied to `<test_package>/adlc_hidden_test.go`
(`HiddenTestPath`); there is no per-bug path.

### Go types (`internal/evals/suite.go`)

```go
package evals

// Difficulty of a seeded bug.
type Difficulty string

const (
	Easy   Difficulty = "easy"
	Medium Difficulty = "medium"
	Hard   Difficulty = "hard"
)

// Meta is the content of a bug's meta.yaml.
type Meta struct {
	ID          string        `yaml:"id"`
	Difficulty  Difficulty    `yaml:"difficulty"`
	Title       string        `yaml:"title"`
	Fixture     string        `yaml:"fixture"`
	TestPackage string        `yaml:"test_package"`
	HiddenTests []string      `yaml:"hidden_tests"`
	Timeout     time.Duration `yaml:"timeout"`
	FixFiles    []string      `yaml:"fix_files"`
}

// Bug is one seeded bug with its files loaded into memory.
type Bug struct {
	Meta
	Dir        string // absolute path of the bug directory
	Issue      string // issue.md content
	BugPatch   []byte
	FixPatch   []byte
	HiddenTest []byte // hidden_test.go content
}

// Fixture is a clean Go module that bugs are applied to.
type Fixture struct {
	Name string
	Dir  string // absolute path
}

// Suite is a loaded eval suite.
type Suite struct {
	Name     string             // base name of the suite directory, e.g. "go-bugs"
	Dir      string             // absolute path
	Fixtures map[string]Fixture // by name; only fixtures some bug uses
	Bugs     []Bug              // sorted by ID
}

// ErrInvalidSuite wraps every validation problem.
var ErrInvalidSuite = errors.New("evals: invalid suite")

// LoadSuite loads and validates the suite in dir. Fixtures are looked up in
// filepath.Join(dir, "..", "..", "fixtures"), i.e. evals/fixtures for
// evals/suites/<name>.
func LoadSuite(dir string) (*Suite, error)

// Bug returns the bug with the given id, or false.
func (s *Suite) Bug(id string) (Bug, bool)
```

Decode `meta.yaml` with `yaml.UnmarshalWithOptions(data, &meta, yaml.DisallowUnknownField())`
from `github.com/goccy/go-yaml` v1.19.2 (it decodes `2m` into `time.Duration`).

### Validation rules (all checked; all problems returned together)

Every problem is an error with the exact text `evals: <bug-dir-name>: <problem>`,
joined with `errors.Join`, and the whole result wrapped as
`fmt.Errorf("%w:\n%w", ErrInvalidSuite, joined)`.

| Check | Problem text |
|-------|--------------|
| `meta.yaml` missing or not parseable | `meta.yaml: <parser error>` |
| `id` differs from the directory name | `id "<id>" does not match directory name` |
| `id` not matching `^[a-z0-9][a-z0-9-]*$` | `id "<id>" must be lowercase letters, digits and dashes` |
| `difficulty` not easy/medium/hard | `difficulty "<d>" must be easy, medium or hard` |
| `title` empty | `title is empty` |
| fixture directory or its `go.mod` missing | `fixture "<f>" not found (looked in <path>)` |
| `test_package` directory missing inside the fixture | `test_package "<p>" not found in fixture "<f>"` |
| `hidden_tests` empty | `hidden_tests is empty` |
| a hidden test name not matching `^TestHidden[A-Za-z0-9_]*$` | `hidden test "<n>" must match ^TestHidden[A-Za-z0-9_]*$` |
| a hidden test name not found as `func <name>(t *testing.T)` in hidden_test.go | `hidden test "<n>" not found in hidden_test.go` |
| `timeout` ≤ 0 or > 10m | `timeout must be between 1s and 10m` |
| `fix_files` empty | `fix_files is empty` |
| a `fix_files` entry not a regular file inside the fixture | `fix file "<p>" not found in fixture "<f>"` |
| `issue.md`, `bug.patch`, `fix.patch` or `hidden_test.go` missing or empty | `<file> is missing or empty` |
| two bugs with the same id (cannot happen with dir names, skip) | — |
| suite has no bug directories | `evals: suite has no bugs` (no bug-dir prefix) |

A "bug directory" is any direct subdirectory of the suite dir. Files directly
in the suite dir (for example a `README.md`) are ignored.

### Workspace preparation (`internal/evals/workspace.go`)

```go
// PrepareOptions controls PrepareWorkspace.
type PrepareOptions struct {
	NoBug          bool // do not apply bug.patch (clean fixture; self-check only)
	WithFix        bool // also apply fix.patch (ignored when NoBug)
	WithHiddenTest bool // also copy hidden_test.go to HiddenTestPath
}

// PrepareWorkspace makes dst a git repository containing the clean fixture
// with bug.patch applied (and, per opts, fix.patch and the hidden test),
// committed as a single commit "base". dst must not exist or be empty.
func (s *Suite) PrepareWorkspace(ctx context.Context, bug Bug, dst string, opts PrepareOptions) error

// HiddenTestPath is where the hidden test goes, relative to the workspace root:
// filepath.Join(bug.TestPackage, "adlc_hidden_test.go").
func HiddenTestPath(bug Bug) string
```

Algorithm of `PrepareWorkspace`:
1. If `dst` exists and is not empty, return `fmt.Errorf("evals: workspace %s is not empty", dst)`.
2. `copyDir(fixture.Dir, dst)`: copy regular files and directories recursively,
   keep file modes, skip any `.git` directory, return an error on symlinks
   (`evals: fixture contains symlink <path>`).
3. `git init -q -b main` in `dst`.
4. Unless `opts.NoBug`, write `bug.patch` to a temp file outside `dst` and run
   `git apply --whitespace=nowarn <tempfile>` in `dst`. On failure return
   `fmt.Errorf("evals: %s: bug.patch does not apply: %s", bug.ID, stderr)`.
5. If `opts.WithFix && !opts.NoBug`, do the same with `fix.patch` (`fix.patch does not apply`).
6. If `opts.WithHiddenTest`, write `bug.HiddenTest` to `filepath.Join(dst, HiddenTestPath(bug))`.
7. `git add -A` then
   `git -c user.name=adlc -c user.email=adlc@localhost -c commit.gpgsign=false commit -q -m base`.

Run every git command with `exec.CommandContext(ctx, "git", args...)`, `cmd.Dir = dst`,
and environment `os.Environ()` plus `GIT_CONFIG_NOSYSTEM=1`, capturing stderr.

The bug is committed **as part of** the single base commit. Never commit the
clean fixture first and the bug second: the agent could then read the bug with
`git log -p`.

### Test output (`internal/evals/testoutput.go`)

```go
// TestOutcome is the result of running a bug's hidden tests.
type TestOutcome struct {
	Passed   []string `json:"passed"`   // hidden tests that printed "--- PASS"
	Failed   []string `json:"failed"`   // hidden tests that printed "--- FAIL"
	Missing  []string `json:"missing"`  // hidden tests that printed neither (did not compile or did not run)
	ExitCode int      `json:"exit_code"`
	TimedOut bool     `json:"timed_out"`
	Output   string   `json:"output"` // last 8 KiB of combined output
}

// AllPassed reports whether every hidden test passed.
func (o TestOutcome) AllPassed() bool // len(Failed)==0 && len(Missing)==0 && ExitCode==0 && !TimedOut

// AllFailed reports whether every hidden test ran and failed.
func (o TestOutcome) AllFailed() bool // len(Passed)==0 && len(Missing)==0 && len(Failed)>0

// HiddenTestArgs returns the go test command line for the bug, run from the
// workspace root: go test -v -count=1 -run '^(A|B)$' ./<test_package>
func HiddenTestArgs(bug Bug) []string

// ParseTestOutput classifies each of bug.HiddenTests from `go test -v` output.
func ParseTestOutput(bug Bug, output []byte, exitCode int, timedOut bool) TestOutcome
```

`HiddenTestArgs` returns exactly
`[]string{"go", "test", "-v", "-count=1", "-run", "^(" + strings.Join(bug.HiddenTests, "|") + ")$", "./" + filepath.ToSlash(filepath.Clean(bug.TestPackage))}`.
For `test_package: .` the last element is `./.`, which `go test` accepts.

`ParseTestOutput`: scan lines; a line matching `^--- (PASS|FAIL): (\S+) ` (no
leading spaces — subtests are indented and ignored) marks that test. A test in
`HiddenTests` that never appears goes to `Missing`. Keep the order of
`HiddenTests` in all three slices. `Output` is the last 8192 bytes.

### Slow self-check (`internal/evals/selfcheck_test.go`)

First line `//go:build evals`. Test `TestSeededBugsBehave` loads
`../../evals/suites/go-bugs` and, for each bug in a subtest named by bug ID
(`t.Parallel()` allowed), checks three workspaces:

| Workspace | Expected |
|-----------|----------|
| `PrepareOptions{NoBug: true, WithHiddenTest: true}` (clean fixture) | `AllPassed()` |
| `PrepareOptions{WithHiddenTest: true}` | first `go test -count=1 -run '^$' ./<pkg>` exits 0 (the test file compiles against the buggy code), then hidden tests `AllFailed()` |
| `PrepareOptions{WithFix: true, WithHiddenTest: true}` | `AllPassed()` |

Run `go test` on the host with `exec.CommandContext`, `cmd.Dir = workspace`,
env `os.Environ()` plus `GOTOOLCHAIN=local`, `GOFLAGS=-mod=mod`, `GOWORK=off`,
and a context timeout of `bug.Timeout`.

## Steps

1. Create the branch from the latest `main` (`AGENTS.md`, "Git and pull requests").
2. Add the dependency: `go get github.com/goccy/go-yaml@v1.19.2` (skip if `go.mod` already has it).
3. Create `internal/evals/testdata/fixtures/tiny/`: `go.mod` (`module tiny`, `go 1.26`) and
   `calc/calc.go` with `package calc` and `func Add(a, b int) int { return a + b }`.
4. Create `internal/evals/testdata/suites/mini/ok-001/` with: `meta.yaml`
   (`id: ok-001`, `difficulty: easy`, `title: Add subtracts`, `fixture: tiny`, `test_package: calc`,
   `hidden_tests` block list with `TestHiddenAddSmall`, `timeout: 1m`, `fix_files` block list with `calc/calc.go`),
   `issue.md` ("Add returns the wrong sum."),
   `bug.patch` (changes `a + b` to `a - b`; produce it with `git diff` in a scratch copy),
   `fix.patch` (changes it back), `hidden_test.go` (`package calc`, `TestHiddenAddSmall` asserting `Add(2, 3) == 5`).
   Note: the testdata path `testdata/suites/mini` → fixtures at `testdata/fixtures`, matching the `../../fixtures` rule.
5. Write `suite.go`: types, `LoadSuite`, `(*Suite).Bug`. Read bug dirs with `os.ReadDir`,
   sort by name, collect problems in a `[]error`.
6. Write `suite_test.go` (see Tests) and make it pass.
7. Write `testoutput.go` and `testoutput_test.go`.
8. Write `workspace.go`; add `TestPrepareWorkspace*` tests (they need `git` on PATH; `t.Skip` if `exec.LookPath("git")` fails).
9. Write `selfcheck_test.go` with the `evals` build tag. It contains two tests with the same body
   (a shared helper `checkSuiteBehaves(t, dir)`): `TestSeededBugsBehave` on `../../evals/suites/go-bugs`
   and `TestMiniSuiteBehaves` on `testdata/suites/mini`. Run `go test -tags evals -run TestMiniSuiteBehaves ./internal/evals/`
   first: it is fast and shows the helper works before the real suite exists or is complete.
10. Add to `Makefile`:
    ```make
    evals-check: ## Check every seeded bug: hidden tests fail on the bug, pass on the fix
    	go test -tags evals -count=1 -run 'TestSeededBugsBehave' ./internal/evals/
    ```
    and add `evals-check` to `.PHONY`. Do **not** add it to `ci` (it is slow).
11. Add a job to `.github/workflows/ci.yml`, same structure as `test`:
    ```yaml
      evals-check:
        runs-on: ubuntu-latest
        steps:
          - uses: actions/checkout@v4
          - uses: actions/setup-go@v5
            with:
              go-version-file: go.mod
          - name: seeded bugs behave
            run: make evals-check
    ```
12. Run the Verify commands, then open the PR.

## Tests

`suite_test.go` (no build tag; uses `testdata/`):
- `TestLoadSuiteMini` — loads `testdata/suites/mini`; 1 bug, `ID == "ok-001"`, `Difficulty == Easy`, `Timeout == time.Minute`, `Issue` non-empty, `Fixtures["tiny"].Dir` ends with `testdata/fixtures/tiny`.
- `TestLoadSuiteValidation` — table test; each case copies the mini suite into `t.TempDir()` (with a sibling `fixtures` dir two levels up, i.e. `<tmp>/suites/mini` and `<tmp>/fixtures/tiny`), breaks one thing, and checks `errors.Is(err, ErrInvalidSuite)` and that `err.Error()` contains the exact problem text from the table above. Cases: id mismatch, bad id chars, bad difficulty, empty title, missing fixture, missing test_package, empty hidden_tests, test name without `TestHidden` prefix, test name not in file, timeout 0, timeout 11m, empty fix_files, fix file not in fixture, missing issue.md, empty bug.patch, unknown meta field (`bogus: 1` → contains `unknown field "bogus"`).
- `TestLoadSuiteReportsAllProblems` — two broken bugs → error text contains both problems.
- `TestLoadSuiteEmpty` — suite dir with no subdirectories → contains `evals: suite has no bugs`.

`testoutput_test.go`:
- `TestHiddenTestArgs` — exact slice for two tests and `test_package: internal/stock`; and `./.` for `.`.
- `TestParseTestOutput` — table: all pass; one pass one fail; subtest lines (`    --- FAIL: TestA/sub`) ignored; compile error output (no `---` lines) → all `Missing`; a non-hidden test's PASS line ignored.
- `TestOutcomeAllPassedAllFailed` — the predicates, including `TimedOut` and non-zero exit with all PASS lines (→ `AllPassed` false).

`suite_test.go`, workspace part:
- `TestPrepareWorkspaceAppliesBug` — mini bug → `calc/calc.go` contains `a - b`; `git log --oneline` has exactly 1 line; hidden test file absent.
- `TestPrepareWorkspaceWithFixAndHiddenTest` — contains `a + b` and `calc/adlc_hidden_test.go`.
- `TestPrepareWorkspaceRejectsNonEmptyDir`.
- `TestPrepareWorkspaceBadPatch` — corrupt bug.patch → error contains `bug.patch does not apply`.

`selfcheck_test.go` (tag `evals`):
- `TestSeededBugsBehave` — as in Design, over `evals/suites/go-bugs`.

## Verify

```bash
make ci
```
Expected: passes; `ok  github.com/roman-ambinder/agentic-framework/internal/evals`.

```bash
make evals-check
```
Expected: `ok` and, with `-v`, one `--- PASS: TestSeededBugsBehave/<id>` per bug (10).

Prove the self-check can fail: temporarily change one bug's `fix.patch` so it
no longer fixes the bug (e.g. delete its last `+` line), run `make evals-check`,
see `FAIL` for that bug, restore the file.

## Out of scope

- Running anything in Docker (task 1.8 does).
- An `adlc eval` command (task 1.8).
- Creating or changing fixtures or bugs (E.1, E.2) — if a real bug fails the self-check, report it; do not fix the bug in this PR.

## Pitfalls

- `go test -run` with a pattern that matches nothing prints `ok … [no tests to run]` and exits 0. That is why `AllPassed` requires a `--- PASS` line for **every** hidden test.
- A hidden test that does not compile against the buggy code "fails" for the wrong reason. The `-run '^$'` compile check catches this.
- Global git config may enable commit signing; always pass `-c commit.gpgsign=false`.
- Running `go test` inside the repo's own module by mistake: the workspace must be outside the repo (`t.TempDir()`), and set `GOWORK=off`.
- The testdata fixture is a separate module (`module tiny`); `go test ./...` of the main repo ignores `testdata` directories, which is what we want.
- Do not print the hidden test or fix patch anywhere the agent could see it (later tasks rely on this module never copying them into an agent workspace except via `WithHiddenTest`/`WithFix`).

## Done checklist

- [ ] `LoadSuite`, `Bug`, `PrepareWorkspace`, `HiddenTestPath`, `HiddenTestArgs`, `ParseTestOutput`, `TestOutcome.AllPassed/AllFailed` exist with the signatures above.
- [ ] All unit tests listed above exist and pass; each was seen failing once.
- [ ] `make evals-check` passes on the real suite; the failure drill above was done.
- [ ] CI has the `evals-check` job; `make ci` does not include it.
- [ ] `make ci` passes.
