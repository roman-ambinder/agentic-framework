# G — Gaps found while building M0/M1

Independent of gate G1: worth fixing whatever M2 turns out to be. Each
section is one task, one branch, one PR. Read `AGENTS.md` and
`docs/impl/interfaces.md` first; the code these build on all exists, so read
it before changing it.

---

## G.1 — Sandbox for `adlc run`

Size: M · Depends on: — · Branch: `task/G.1-run-sandbox` · PR title: `G.1: adlc run can run an agent in a sandbox`

**Goal.** Today only `adlc eval` and `adlc resume` provide a sandbox, so an
agent file whose tool commands contain `{{sandbox_id}}`
(`sandbox.SandboxIDPlaceholder`) cannot be used with `adlc run`. After this
task, an agent file can declare a sandbox and `adlc run` creates it.

**Agent file.** New optional top-level section, parsed and validated in
`internal/agentconfig`:

```yaml
sandbox:
  image: golang:1.26        # optional; default "golang:1.26"
  workspace: ./workspace    # required; host dir, relative to the agent file
  network: false            # optional; default false
  cpus: 2                   # optional; >= 0; 0 = no limit
  memory_mb: 4096           # optional; >= 0; 0 = no limit
```

Validation (all errors reported together, as the existing validation does):
- `sandbox.workspace` is required when `sandbox` is present, and must be an existing directory.
- If any tool command argument contains `{{sandbox_id}}`, `sandbox` is required:
  `tools[N].command: uses {{sandbox_id}} but the agent has no sandbox section`.
- `cpus` and `memory_mb` must not be negative.

**`adlc run` behaviour** (in `internal/cli`, reusing `buildRun`):
1. If the agent has no `sandbox`, nothing changes.
2. Otherwise create the Docker backend; if Docker is unavailable, exit 1 with `run: docker is not available: <err>`.
3. `Create` a sandbox with `RunID` = the run id, the image, the workspace, network, CPUs and memory.
4. Replace `{{sandbox_id}}` in every tool command with `sandbox.ExpandArgs`, then `buildRun` and run as today.
5. After the run: if the stop reason is `AWAITING_APPROVAL`, keep the container (so `adlc resume` finds it with `FindByRunID`) and print `sandbox: kept <id> for resume`. Otherwise copy `/workspace` out to `--workspace-out` (default `.adlc/workspaces/<run-id>/`), print `workspace: <dir>`, and destroy the container unless `--keep-sandbox` is set.
6. If the process crashes, the container stays (labelled with the run id), which is what `adlc resume` expects.

**Tests.** Make the sandbox backend injectable in the CLI (an interface with
`Create`, `CopyOut`, `Destroy` and the existing `FindByRunID`, defaulting to
the Docker backend), and unit-test steps 2–5 with `sandboxtest`. Add one
Docker-tagged end-to-end test (`//go:build docker`): fake model script, the
`shell__run` tool writes a file, the copied-out workspace contains it. Add
`examples/sandbox-hello.yaml` and its fake script; `TestExampleFilesLoad` loads it.

**Out of scope.** Network allow-lists, sandbox reuse across runs.

---

## G.2 — Hook plugins declared in the agent file

Size: M · Depends on: — · Branch: `task/G.2-agent-hooks` · PR title: `G.2: agent files can declare hook plugins`

**Goal.** Task 0.11 built out-of-process hook plugins (`internal/plugin`),
but nothing starts them from an agent file. After this task, `adlc run` and
`adlc resume` start the hooks an agent declares and pass them to the harness.

**Agent file.** New optional top-level list, parsed and validated in
`internal/agentconfig`; the fields map onto `plugin.Config` (read
`internal/plugin` for the exact names and defaults and use them):

```yaml
hooks:
  - name: deny-writes                 # required; the harness's hook name rules
    command: ["python3", "hook_plugin.py"]   # required; resolved like tool commands
    env: {ADLC_HOOK_DENY_PATTERN: "fs__write_.*"}   # optional
    start_timeout: 5s                 # optional; plugin default if unset
    call_timeout: 10s                 # optional; plugin default if unset
```

Validation: names unique and valid; command non-empty; durations positive.

**Behaviour.** In the CLI wiring used by both `run` and `resume`: start
every hook with the plugin launcher in declared order, put them in
`harness.Config.Hooks`, and close every started plugin when the command
returns (also on errors and when a later hook fails to start). A hook that
fails to start fails the command with exit 1 and
`run: hook <name>: <err>`, and no run is started.

**Tests.** Make the plugin starter injectable so CLI tests use an in-process
fake hook; test start order, close-on-every-path, the start-failure message,
and that a denying hook ends a fake-model run `DENIED`. Update
`examples/plugins/python-hook/README.md` with an agent-file example.

**Out of scope.** Hooks for `adlc eval`.

---

## G.3 — CLI consistency

Size: S · Depends on: — · Branch: `task/G.3-cli-consistency` · PR title: `G.3: one --db default and positional agent for resume`

1. One constant `defaultDBPath = ".adlc/events.db"` in `internal/cli`, used by every command's `--db` flag; remove `traceDBDefault` and the string literals.
2. `adlc resume RUN_ID [AGENT_YAML]`: accept the agent file as a second positional argument, like `adlc run AGENT_YAML`. Keep `--agent` working. Errors: neither given → `resume: an agent file is required (AGENT_YAML or --agent)`; both given and different → `resume: AGENT_YAML and --agent disagree`.
3. Tests for all three cases plus the shared default.

---

## G.4 — Context manager memory keyed by position

Size: S · Depends on: — · Branch: `task/G.4-contextmgr-keys` · PR title: `G.4: remember elided tool results by position`

`contextmgr.Manager` remembers elided tool results by `CallID`, which is only
safe if every tool call id in a run is unique; providers do not guarantee
that across a long run. Messages are only ever appended, so a result's
position (message index, part index) is stable: key the memory by position
instead. Test: a transcript where two results share a `CallID`, only the
older one is elided, and the newer one must stay intact on the next
`Prepare`. Keep every existing contextmgr and harness test passing.
