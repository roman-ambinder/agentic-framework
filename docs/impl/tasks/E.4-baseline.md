# E.4 — Manual baseline

Size: S · Depends on: E.3 · Branch: `task/E.4-baseline` · PR title: `E.4: manual baseline on the go-bugs suite and gate G1 thresholds`

## Goal

A person (not code) runs every seeded bug past a general-purpose coding agent
by hand, records the results in a fixed table, and uses them to (a) judge
whether the suite is too easy or too hard and (b) set the numeric thresholds
of gate G1 in the work plan. No Go code is written in this task.

## Read first

- `AGENTS.md`
- `docs/plans/2026-11-13-adlc-work-plan.md` §5 (gate G1 table) and §8 (decision log)
- `docs/impl/tasks/E.3-fixture-loader.md` (workspace format, hidden test location)
- Every `evals/suites/go-bugs/*/meta.yaml`

## Files

Create:
- `evals/baselines/<YYYY-MM-DD>-manual.md` — the results (date = the day you start)

Change:
- `docs/plans/2026-11-13-adlc-work-plan.md` — §5 thresholds, §8 decision log row

## Design

### What "a general-purpose coding agent" means here

Any interactive AI coding assistant that can read and edit files and run
shell commands in a local directory. Use **one** agent and **one** model for
the whole baseline, and write both down. Give it nothing but the issue text
and the prompt below. Do not help it.

### The prompt (use exactly this, with the issue pasted in)

```
You are working in a Go repository in the current directory.
A user reported the following issue. Fix it. Run the tests to check your fix.
When you are done, reply with one sentence describing the fix.

--- ISSUE ---
<contents of issue.md>
```

### Limits per bug

- Wall-clock time box: **15 minutes** from sending the prompt.
- If the agent asks a question, answer only: `Use your best judgement.`
- If it stops before fixing, send once: `Please continue until the issue is fixed.` Then no more help.

### Results table (exact format)

`evals/baselines/<date>-manual.md` starts with:

```markdown
# Manual baseline — <date>

- Agent: <name and version>
- Model: <model as shown by the agent>
- Suite: evals/suites/go-bugs at commit <git rev-parse --short HEAD>
- Time box: 15 minutes per bug

| Bug | Difficulty | Result | Minutes | Cost (USD) | Files changed | Notes |
|-----|------------|--------|---------|------------|---------------|-------|
```

One row per bug, in bug-ID order:
- **Result**: `pass` if all hidden tests pass (see Steps), else `fail`, or `timeout` if the time box ran out.
- **Minutes**: whole minutes from prompt to the agent's final reply (or `15` for timeout).
- **Cost**: as reported by the agent if it shows cost; otherwise `n/a`.
- **Files changed**: output of `git diff --name-only HEAD | wc -l` in the workspace.
- **Notes**: one short phrase: what went wrong, or `—`.

After the table, add:

```markdown
## Summary

| Difficulty | Bugs | Passed | Pass rate |
|------------|------|--------|-----------|
| easy | | | |
| medium | | | |
| hard | | | |
| all | | | |

Median minutes (passed bugs only): <n>
Median cost (passed bugs only): <n or n/a>
```

### Suite calibration rule

| Overall baseline pass rate | Meaning | Action |
|----------------------------|---------|--------|
| 10/10 | too easy: G1 cannot tell good agents from bad | Stop. Report that E.2 needs at least 3 harder bugs; do not set thresholds. |
| 0/10 – 2/10 | too hard, or bugs/issues are broken | Stop. Re-read each failing issue: can a human fix it from the issue alone? Report which bugs to rewrite. |
| 3/10 – 9/10 | usable | Continue to thresholds. |

### Gate G1 threshold rule

Let `B` = baseline overall pass rate (e.g. 0.7) and `C` = baseline median cost
of passed bugs (if `n/a`, leave the cost threshold for the person to set and
write `to be set` in the plan).

- **pass@1 is acceptable** if the coder agent's pass@1 on the full suite (best provider) is ≥ `max(B − 0.2, 0.3)`, rounded down to the nearest 0.1.
- **cost is acceptable** if the coder agent's median cost per solved bug is ≤ `2 × C`.
- **steps are acceptable** if the median steps per solved bug is ≤ 40 (fixed; the baseline has no step count).

Example: B = 0.7, C = $0.40 → pass@1 ≥ 0.5, median cost ≤ $0.80, median steps ≤ 40.

## Steps

1. Create the branch from the latest `main`.
2. Pick the agent and model; note their names and versions.
3. For each bug ID (in order), prepare a fresh buggy workspace with these commands
   from the repository root (replace `<id>`):
   ```bash
   BUG=evals/suites/go-bugs/<id>
   FIX=$(sed -n 's/^fixture: *//p' "$BUG/meta.yaml")
   W=$HOME/adlc-baseline/<id>
   mkdir -p "$W"
   cp -R "evals/fixtures/$FIX/." "$W/"
   git -C "$W" init -q -b main
   git -C "$W" apply --whitespace=nowarn "$PWD/$BUG/bug.patch"
   git -C "$W" add -A
   git -C "$W" -c user.name=adlc -c user.email=adlc@localhost -c commit.gpgsign=false commit -q -m base
   ```
   Check: `git -C "$W" log --oneline` shows exactly one commit. `$W` must be outside the repository.
4. Start the coding agent **in `$W`** (never in the repository, which contains the hidden tests and fixes). Send the prompt with `issue.md` pasted in. Start a timer.
5. When the agent finishes (or at 15 minutes), stop it. Record Minutes and Cost.
6. Grade, from the repository root:
   ```bash
   PKG=$(sed -n 's/^test_package: *//p' "$BUG/meta.yaml")
   TESTS=$(sed -n '/^hidden_tests:/,/^[a-z_]*:/p' "$BUG/meta.yaml" | sed -n 's/^ *- *//p' | paste -sd'|')
   cp "$BUG/hidden_test.go" "$W/$PKG/adlc_hidden_test.go"
   (cd "$W" && GOTOOLCHAIN=local GOWORK=off go test -v -count=1 -run "^($TESTS)\$" "./$PKG")
   ```
   The `TESTS` line expects `hidden_tests` written as a block list (`- Name` per line), as in the E.3 example;
   check with `echo "$TESTS"` that it prints the names joined by `|`.
   `pass` only if every hidden test name prints `--- PASS`. Then `git -C "$W" diff --name-only HEAD | wc -l` for Files changed (count before copying the hidden test, or subtract it).
7. Fill the row. Repeat 3–7 for every bug.
8. Fill the Summary tables. Apply the calibration rule. If it says Stop, commit the results file only, open the PR, and say in the PR description which action is needed.
9. Otherwise compute the three thresholds with the rule above.
10. In the work plan §5, replace the line `Set the numeric thresholds after E.4, once the suite's difficulty is known.` with:
    ```markdown
    Thresholds (from the E.4 baseline, `evals/baselines/<date>-manual.md`):
    pass@1 ≥ <x> · median cost per solved bug ≤ $<y> · median steps per solved bug ≤ 40.
    "Low pass@1" in the table below means below <x>; "high cost or steps" means above these limits.
    ```
11. In the work plan §8, add a row: `| <date> | E.4 baseline | B = <B>, thresholds pass@1 ≥ <x>, cost ≤ $<y>, steps ≤ 40 |`.
12. Run `make ci` (docs-only change, must still pass) and open the PR.

## Tests

None (no code). The results file itself is the evidence; the PR reviewer checks
that every bug has a row and the summary numbers add up.

## Verify

```bash
grep -c '^| ' evals/baselines/*-manual.md
```
Expected: 1 header + 1 separator + 10 bug rows + summary rows (header, separator, 4) = at least 18.

```bash
grep -n 'Thresholds (from the E.4 baseline' docs/plans/2026-11-13-adlc-work-plan.md
```
Expected: one match (unless the calibration rule said Stop).

## Out of scope

- Writing scripts or Go code to automate the baseline (task 1.8 automates evaluation).
- Changing bugs or issue texts, even if one looks broken: report it in the PR.
- Running the baseline with more than one agent or model.

## Pitfalls

- Starting the agent inside the repository lets it read `hidden_test.go` and `fix.patch`. Always use `$W` outside the repository.
- Copying the hidden test before counting changed files inflates the count by one.
- Agents often run `go mod tidy` or try to download modules; the fixture is stdlib-only, so a network error there does not matter, but note it.
- Do not re-run a bug to get a better result. One attempt per bug: that is what pass@1 means.

## Done checklist

- [ ] `evals/baselines/<date>-manual.md` has 10 rows and the summary in the exact format.
- [ ] Calibration rule applied and its outcome stated in the PR description.
- [ ] Thresholds written into the work plan §5 and a decision-log row in §8 (unless Stop).
- [ ] `make ci` passes.
