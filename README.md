# adlc

A Go framework for the Agentic Development Life Cycle: a general agent platform
(harness, tools, memory, orchestrator, sandbox, control plane, observability and
evaluation) with an SDLC agent team as its first workload.

Status: early development (milestone M0).

- Design: [docs/design/2026-11-13-adlc-framework-design.md](docs/design/2026-11-13-adlc-framework-design.md)
- Build plan: [docs/plans/2026-11-13-adlc-m0-m1-plan.md](docs/plans/2026-11-13-adlc-m0-m1-plan.md)

## Development

Requires Go 1.21 or newer; `make` downloads Go 1.26.11 and all tools on first use.

```bash
make build        # bin/adlc
make test         # unit tests
make lint         # golangci-lint
make ci           # everything CI runs
```

Contracts (`contracts/proto`) are the language-neutral source of truth. Go code
is generated into `gen/go` and committed. `buf` and `protoc-gen-go` are pinned
in `tooling/go.mod`, so no separate install is needed.

```bash
make proto            # regenerate gen/go after editing a .proto file
make proto-lint       # buf lint
make proto-breaking   # compatibility check against main (or AGAINST=<dir|ref>)
```

```bash
./bin/adlc version
```

## Run the example

`adlc run` loads an agent file, starts its tool servers, runs the agent and
prints a summary. Exit code 0 means the run completed, 2 means it stopped for
another reason, 1 means it could not run. The first command uses a scripted
fake model; the second calls the Anthropic API (needs `ANTHROPIC_API_KEY`).

```bash
make build
./bin/adlc run examples/hello.yaml --task "What does greeting.txt say?" \
  --fake-script examples/hello.fake.json --db bin/demo.db
ADLC_ANTHROPIC_MODEL=<model id> ./bin/adlc run examples/hello-anthropic.yaml \
  --task "What does greeting.txt say?" --db bin/demo.db
```
