# Gate G0: a real model through the agent loop

Gate G0 (work plan §3) asks: does a real model complete a simple file task
through the loop without harness bugs? `scripts/gate-g0.sh` runs it.

## 1. Set the variables

In the cloud environment used for this repository (`agentic-framework`:
the environment menu in the session's title bar, then Edit), add these
environment variables, then start a **new** session (running sessions do not
pick up new variables):

| Variable | Value |
|----------|-------|
| `ANTHROPIC_API_KEY` | your API key (stored in plain text on the environment) |
| `ADLC_ANTHROPIC_MODEL` | the model id you want to test |
| `ADLC_ANTHROPIC_PRICE_INPUT` | USD per million input tokens for that model, e.g. `3.00` |
| `ADLC_ANTHROPIC_PRICE_OUTPUT` | USD per million output tokens |
| `ADLC_ANTHROPIC_PRICE_CACHE_READ` | USD per million cache-read tokens (`0` if unsure) |
| `ADLC_ANTHROPIC_PRICE_CACHE_WRITE` | USD per million cache-write tokens (`0` if unsure) |

Never commit any of these values (AGENTS.md).

## 2. Run it

```bash
scripts/gate-g0.sh
```

It checks every variable first and stops without spending anything if one is
missing. Then it runs the live tests (`TestLive*` in `internal/model/anthropic`
and `internal/harness`), runs `examples/hello-anthropic.yaml` once with the
real model, and prints the run's trace and cost. Outputs are saved in
`bin/gate-g0/` (git-ignored). Expected spend: a few small requests; the hello
agent's own budget is 0.10 USD.

## 3. Record the result

Add a row to the work plan's decision log (§8):

```
| <date> | Gate G0 | stop <stop reason>, steps <n>, tool calls <n>, tokens in <n> out <n>, cost $<x>; live tests <pass/fail> → <proceed to M1 / fix the loop first> |
```

G0 passes when the run stops with `completed`, the answer quotes
`hello, world`, and the trace shows the `fs__read_file` call. Any other stop
reason is a harness finding to fix before M1's evals mean anything.
