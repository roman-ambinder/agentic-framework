# Running the live eval on GitHub

The workflow `.github/workflows/eval-live.yml` runs `adlc eval` on the
`go-bugs` suite with real models. It costs real money, so it only starts when
you trigger it by hand (never on push or pull request).

You choose the model and you set its prices. Nothing about the model or its
price is committed: the agent files read them from environment variables, and
the workflow reads those from repository variables.

## One-time setup

In the repository on GitHub: **Settings → Secrets and variables → Actions**.

**Secrets** (tab "Secrets" → New repository secret):

| Name | Value |
|------|-------|
| `ANTHROPIC_API_KEY` | your Anthropic API key |
| `OPENAI_API_KEY` | your OpenAI API key (only needed to run the OpenAI agent) |

**Variables** (tab "Variables" → New repository variable). Add the group for
each provider you run:

| Name | Value |
|------|-------|
| `ADLC_ANTHROPIC_MODEL` | the Anthropic model id you want to evaluate |
| `ADLC_ANTHROPIC_PRICE_INPUT` | USD per million input tokens, e.g. `3.00` |
| `ADLC_ANTHROPIC_PRICE_OUTPUT` | USD per million output tokens |
| `ADLC_ANTHROPIC_PRICE_CACHE_READ` | USD per million cache-read tokens |
| `ADLC_ANTHROPIC_PRICE_CACHE_WRITE` | USD per million cache-write tokens |
| `ADLC_OPENAI_MODEL` | the OpenAI model id you want to evaluate |
| `ADLC_OPENAI_PRICE_INPUT` / `_OUTPUT` / `_CACHE_READ` / `_CACHE_WRITE` | same four prices for OpenAI |

Prices are decimal strings in USD per million tokens, copied from the
provider's price page for the model you chose (at most 6 decimals, no currency
sign). The cost numbers in the reports and the spend limits are computed from
them, so wrong prices mean wrong costs. If you change the model, change its
prices too.

## Start a run

**Actions → eval-live → Run workflow.** Inputs:

- `agents`: agent files, comma separated. Default
  `agents/sdlc/coder.anthropic.yaml`. For both providers:
  `agents/sdlc/coder.anthropic.yaml,agents/sdlc/coder.openai.yaml`.
- `bugs`: bug ids, comma separated; empty means all 10 bugs.
- `max_total_usd`: spend limit, default `10`.

Before any model call, the job computes the worst case: bugs × agent files ×
each agent's `budget.max_usd`, and fails if it is above `max_total_usd`.
All 10 bugs with one coder agent (`max_usd` 2.00) is 20 USD in the worst case,
so raise `max_total_usd` (for example to `20`) or select fewer bugs with
`bugs`. You can run the same check on your machine without Docker or API keys:

```bash
./bin/adlc eval evals/suites/go-bugs --agents agents/sdlc/coder.anthropic.yaml --dry-run
# bugs=10 agents=1 worst_case_usd=20.000000
```

(set the `ADLC_*` variables in your shell first).

## Read the results

The job log ends with the result table (`adlc eval-report`). The artifact
`eval-results` on the run page holds `eval-report.json` and `eval.db`
(the event log of every run).

```bash
# unzip the downloaded eval-results.zip into a new folder, then:
./bin/adlc eval-report eval-results/eval-report.json
# run_id of a failed bug: open eval-report.json and read the "run_id" field
./bin/adlc trace <run_id> --db eval-results/eval.db
```

`adlc trace` prints the steps of that run (model calls, tool calls, stop
reason); use it to find out why a bug was not solved.
