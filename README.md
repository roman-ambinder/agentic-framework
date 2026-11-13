# adlc

A Go framework for the Agentic Development Life Cycle: a general agent platform
(harness, tools, memory, orchestrator, sandbox, control plane, observability and
evaluation) with an SDLC agent team as its first workload.

Status: early development (milestone M0).

- Design: [docs/design/2026-11-13-adlc-framework-design.md](docs/design/2026-11-13-adlc-framework-design.md)
- Build plan: [docs/plans/2026-11-13-adlc-m0-m1-plan.md](docs/plans/2026-11-13-adlc-m0-m1-plan.md)

## Development

Requires Go 1.24+ and golangci-lint v2.

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
