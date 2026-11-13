# Eval suites

A suite is a set of tasks for an agent. `go-bugs/` holds seeded bugs for the
`ledger` fixture in `evals/fixtures/ledger/`. Each bug is a directory with a
user-style issue, a patch that introduces the bug into the clean fixture, a
reference fix and a hidden test. The agent sees only the buggy repository and
`issue.md`; it never sees `hidden_test.go`, `fix.patch` or `meta.yaml`.

`go.mod` in this directory (module `example.com/evalsuites`) exists so that the
Go tool of the main module skips this tree: the hidden tests do not compile on
their own, only inside a copy of the fixture.

## Difficulty levels

| Level | Definition |
|-------|-----------|
| easy | The cause is in one function. The issue names the operation that misbehaves (e.g. "splitting"). Fix is 5 lines or fewer. |
| medium | The cause is in one file, but the issue describes higher-level behaviour (a statement, an import), so the solver must trace through 2-3 packages. Fix is about 10 lines or fewer. |
| hard | Needs an invariant that spans 2 or more files or packages. The issue describes a downstream symptom, and the first obvious suspect is not the cause. |

## Bug directory format

```
go-bugs/<id>/
  issue.md          "# <title>", a blank line, then the user's report
  bug.patch         `git diff --relative` of the bug, applied with `git apply -p1`
  fix.patch         the reference fix, relative to the bugged tree (normally bug.patch reversed)
  hidden_test.go    one external test file (package <pkg>_test); every test is named TestHidden...
  meta.yaml         id, difficulty, title, fixture, test_package, hidden_tests, timeout, fix_files
```

- `issue.md` of a medium or hard bug never names files, functions or tests.
- `meta.yaml` is defined in `docs/impl/tasks/E.3-fixture-loader.md`. Keep
  top-level keys unindented and lists as block lists; `check-bug.sh` reads the
  `test_package:` line with `sed`. `id` equals the directory name.
- The hidden test is copied to `<test_package>/adlc_hidden_test.go`. It may use
  only the fixture's public API, and it must fail because of the bug, not
  because it does not compile.
- With the bug applied, `go vet` and every visible test of the fixture must still
  pass. If a visible test fails, it covers a behaviour that should be reserved
  for the hidden test: change the fixture's tests, not the bug.

## Adding a bug

```bash
cd evals/fixtures/ledger
# 1. edit the fixture to introduce the bug
mkdir -p ../../suites/go-bugs/<id>
git diff --relative -- . > ../../suites/go-bugs/<id>/bug.patch
git diff --relative -R -- . > ../../suites/go-bugs/<id>/fix.patch
git checkout -- .                 # back to the clean fixture
cd ../../..
# 2. write issue.md, hidden_test.go and meta.yaml
evals/scripts/check-bug.sh <id>   # all six lines must say ok
```

`make bugs-check` runs the script for every bug. For each bug it checks that the
hidden test passes on the clean fixture, the bug applies, `go vet` and the visible
tests pass with the bug, the hidden test fails with the bug, the fix applies, and all
tests pass with the fix. Never commit a bug applied to `evals/fixtures/ledger`.
