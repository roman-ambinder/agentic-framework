# AGENTS.md — instructions for coding agents working in this repository

Read this file completely before you change anything. Then read the brief for
your task in `docs/impl/tasks/` and the shared spec in `docs/impl/interfaces.md`.

## What this project is

`adlc` is a Go framework for running AI agents through a software development
life cycle. One binary (`cmd/adlc`) contains the agent loop ("harness"), tool
routing (MCP), a policy check, an append-only event log (SQLite) and, later, a
sandbox, an orchestrator and a control plane.

- Design: `docs/design/2026-11-13-adlc-framework-design.md`
- Work plan (order of tasks, milestones, gates): `docs/plans/2026-11-13-adlc-work-plan.md`
- Per-task instructions: `docs/impl/tasks/<id>-<name>.md`
- Shared Go types and interfaces: `docs/impl/interfaces.md` (**this is the contract; do not invent different names**)

## Repository layout

```
cmd/adlc/                 main package (calls internal/cli)
contracts/proto/adlc/v1/  protobuf contracts (source of truth)
contracts/                contract tests for the generated code
gen/go/adlc/v1/           generated Go code (never edit by hand; run `make proto`)
internal/buildinfo/       version info
internal/cli/             cobra commands
internal/eventlog/        append-only event log (SQLite) and fold to RunState
internal/...              other packages, one per brief
tooling/go.mod            pinned developer tools (buf, protoc-gen-go, ...)
docs/                     design, plans, implementation briefs
```

## Commands

| Command | What it does |
|---------|--------------|
| `make build` | builds `bin/adlc` |
| `make test` | unit tests |
| `make test-race` | unit tests with the race detector |
| `make lint` | golangci-lint |
| `make fmt` | formats code (gofumpt + goimports). Run before `make lint` |
| `make proto` | regenerates `gen/go` after you edit a `.proto` file |
| `make ci` | everything CI runs. **Must pass before you commit** |

Run one package's tests: `go test ./internal/eventlog/ -run TestName -v`.

## Rules that have already caused bugs (follow them exactly)

1. **Go version.** The Go version is pinned in `go.mod` and the `Makefile`
   (`GOTOOLCHAIN`). Never change it unless your brief says so.
2. **Never run `go mod tidy` inside `tooling/`.** It upgrades dependencies to
   versions that need a newer Go. Change a tool's version only with
   `cd tooling && go get -tool <module>@<version>`.
3. **Adding a dependency:** use the exact version your brief names:
   `go get <module>@<version>`, then `go mod tidy` in the repo root. If that
   version does not exist or does not build, stop and report; do not pick
   another version on your own.
4. **Generated code:** never edit files under `gen/`. Edit the `.proto` file
   and run `make proto`. CI fails if `gen/` is stale.
5. **Protobuf compatibility:** never renumber, rename or remove a field or enum
   value in `contracts/proto`. Only add. `make proto-breaking` checks this.
6. **Git history:** when you fetch, always use `--depth=50` or more, never
   `--depth=1`. A depth-1 fetch once produced a commit with no parent.
7. **Do not delete with a variable path** (`rm -rf $DIR`). Use fixed paths.

## Code conventions

- Errors: wrap with context, prefix with the package name:
  `fmt.Errorf("eventlog: read last seq: %w", err)`. Exported sentinel errors
  are `var ErrXxx = errors.New("pkg: ...")` and are checked with `errors.Is`.
- No global state. Pass dependencies in structs (see `docs/impl/interfaces.md`).
- Time: never call `time.Now()` directly in logic you test; take a
  `func() time.Time` (default `time.Now`). Same for sleeping: take a
  `func(ctx context.Context, d time.Duration) error`.
- Every exported identifier has a doc comment that starts with its name.
- Keep functions short; if a function needs a comment per block, split it.
- Accept `context.Context` as the first parameter of anything that does I/O.

## Testing conventions

- Table-driven tests with `t.Run(tt.name, ...)`.
- Use `t.TempDir()` for files and databases; never write outside it.
- Tests never use the network and never call a real model. Real API calls
  live in files with `//go:build live` and run only with `go test -tags live`.
- Never write a real model id into the repository. Agent files use
  `name: ${ADLC_ANTHROPIC_MODEL}` / `${ADLC_OPENAI_MODEL}`, and live tests read
  the same environment variables (skip when unset).
- Use the scripted fake model (`internal/model`, `model.NewFake`) for harness tests.
- A test name says what it checks: `TestAppendRejectsSecondRunStarted`.
- When you fix a bug, first add a test that fails, then fix.
- Before you finish, check your tests can fail: break the code on purpose,
  see the test fail, restore it.

## Git and pull requests

- One task = one branch = one PR. Branch from the latest `main`:
  `git fetch --depth=50 origin main && git checkout -b <branch> origin/main`.
  Branch names are given in each brief.
- Never push to `main`. Never force-push unless a person asks you to.
- Commit messages: first line `<task id>: <summary>` (max 72 chars), blank
  line, then what changed and why, as bullet points.
- **Do not add AI-assistant attribution anywhere**: no co-author trailers, no
  "generated by" lines or footers, no session links, in commits, PR titles,
  PR descriptions, code, comments or docs.
- PR description sections: `## Summary`, `## Testing` (commands you ran and
  their results), `## Notes` (anything a reviewer should know).

## Definition of done (every task)

- [ ] Everything in the brief's "Done checklist" is ticked.
- [ ] `make ci` passes.
- [ ] New code has tests; you saw each new test fail once by breaking the code.
- [ ] No TODOs left without a matching item in the brief's "Out of scope".
- [ ] Docs updated if behaviour differs from the design doc.

## When to stop and ask instead of guessing

- The brief and the existing code disagree.
- A library function the brief names does not exist or has a different signature.
- You need a dependency, network access or a credential the brief does not mention.
- A test can only pass by weakening or skipping another test.
- The task looks bigger than its size (S/M/L) suggests.

Write down what you found and what you would propose; do not work around it.
