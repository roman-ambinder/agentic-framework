# Python hook plugin

An example `adlc.v1.HookService` plugin written in Python. It denies tool
calls whose name matches a regular expression, and can log finished calls.
It proves that the plugin contract (`contracts/proto/adlc/v1/plugin.proto`)
does not depend on Go.

## Configuration

| Variable | Meaning |
|----------|---------|
| `ADLC_PLUGIN_SOCKET` | Unix socket path to listen on. Set by the launcher (`internal/plugin`). Required. |
| `ADLC_HOOK_DENY_PATTERN` | Python regular expression. A tool call whose name matches it with `re.fullmatch` is denied. Unset or empty: allow everything. |
| `ADLC_HOOK_LOG` | Optional file path. Each `PostTool` appends one JSON line `{"tool": ..., "is_error": ..., "exit_code": ...}`. |

Exit codes: 2 for a missing socket variable or an invalid pattern, 1 if the
socket cannot be opened.

## Use in an agent file

Declare the plugin under `hooks:` in `agent.yaml`. `adlc run` and
`adlc resume` start it before the run and stop it when the command returns.

```yaml
hooks:
  - name: deny-writes
    command: ["python3", "hook_plugin.py"]   # runs in the agent file's directory
    env: {ADLC_HOOK_DENY_PATTERN: "fs__write_.*"}
    start_timeout: 5s    # optional
    call_timeout: 10s    # optional
```

Hooks run in the order listed. If a hook fails to start, the command exits 1
with `run: hook <name>: <error>` and no run is started.

## Setup

```
make python-hook-setup
```

This creates `.venv/` and generates the Python stubs into `gen/` from
`contracts/proto`. Both are git-ignored; the stubs are generated, never
committed, so they cannot go stale.

## Test

```
make test-plugin-python
```

Runs the Go tests tagged `plugin_python` in `internal/plugin`, including a
harness run on the fake model that this plugin stops. Without the setup the
tests skip.
