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
