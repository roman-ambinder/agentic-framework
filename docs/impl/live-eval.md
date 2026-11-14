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
- `repeat`: attempts per bug and agent, 1 to 20, default `1`. With more than one
  attempt the report adds pass@k; a single attempt per bug is a noisy measure
  on a 10-bug suite.
- `max_total_usd`: spend limit, default `10`.

Before any model call, the job computes the worst case: bugs × agent files ×
`repeat` × each agent's `budget.max_usd`, and fails if it is above `max_total_usd`.
All 10 bugs with one coder agent (`max_usd` 2.00) is 20 USD in the worst case,
so raise `max_total_usd` (for example to `20`) or select fewer bugs with
`bugs`. You can run the same check on your machine without Docker or API keys:

```bash
./bin/adlc eval evals/suites/go-bugs --agents agents/sdlc/coder.anthropic.yaml --repeat 3 --dry-run
# bugs=10 agents=1 repeat=3 worst_case_usd=60.000000
```

(set the `ADLC_*` variables in your shell first).

## Read the results

The job log ends with the result table and the failure analysis
(`adlc eval-report --failures`: one cause per unsolved result, with evidence,
and counts per agent). The artifact `eval-results` on the run page holds
`eval-report.json`, `eval.db` (the event log of every run) and
`eval-failures.json` (the failure analysis).

```bash
# unzip the downloaded eval-results.zip into a new folder, then:
./bin/adlc eval-report eval-results/eval-report.json
# run_id of a failed bug: open eval-report.json and read the "run_id" field
./bin/adlc trace <run_id> --db eval-results/eval.db
```

`adlc trace` prints the steps of that run (model calls, tool calls, stop
reason); use it to find out why a bug was not solved.

To compare two runs (for example before and after a prompt change, task 1.10):

```bash
./bin/adlc eval-compare base/eval-report.json new/eval-report.json
```

It prints pass@1 (and pass@k when both runs used `repeat`), median cost and
median steps per difficulty and agent, with the change, and the bugs whose
status changed.

## Gate G1

Gate G1 (work plan §5) decides what M2 is. To measure it:

1. Run E.4 first (`docs/impl/tasks/E.4-baseline.md`): it sets the numbers G1 is judged against.
2. Trigger **eval-live** with all bugs, `repeat` = 3 and a `max_total_usd`
   covering the worst case the spend guard prints (60 USD for one coder agent).
   Use fewer bugs or `repeat` = 2 to spend less; the measurement gets noisier.
3. From the job log, record per agent: pass@1 per difficulty and pass@3
   (result table), median cost and median steps, and the top 3 failure
   causes (failure analysis counts).
4. Add the row to the work plan's decision log (§8) as task 1.10 describes,
   and pick the matching row of the G1 table: that decides M2-Q or M2-C
   (`docs/impl/m2.md`).
