# E.2 — Seeded bugs

Size: L · Depends on: E.1 · Branch: `task/E.2-seeded-bugs` · PR title: `E.2: ten seeded bugs for the ledger fixture`

## Goal

After this task, `evals/suites/go-bugs/` holds 10 bugs for the ledger fixture
(3 easy, 4 medium, 3 hard). Each bug has an issue written the way a user would
report it, a patch that introduces the bug, a reference fix, and a hidden test
that fails with the bug and passes without it. A script proves all four
properties for every bug.

## Read first

- `AGENTS.md`
- `docs/impl/tasks/E.1-fixture-repo.md` (the fixture's design and its "Reserved behaviours" list)
- The merged code in `evals/fixtures/ledger/`
- `docs/impl/tasks/E.3-fixture-loader.md` (reads the files you produce)

## Files

```
evals/suites/go.mod                          module example.com/evalsuites, go 1.26 (see Pitfalls)
evals/suites/README.md                       what a suite is, the format below, how to add a bug
evals/suites/go-bugs/<id>/issue.md
evals/suites/go-bugs/<id>/bug.patch
evals/suites/go-bugs/<id>/fix.patch
evals/suites/go-bugs/<id>/hidden_test.go
evals/suites/go-bugs/<id>/meta.yaml
evals/scripts/check-bug.sh
```

Change: `Makefile` — target `bugs-check` (not part of `ci`; task E.3 adds the CI job `evals-check`).

## Design

### Difficulty levels

| Level | Definition |
|-------|-----------|
| easy | The cause is in one function. The issue names the operation that misbehaves (e.g. "splitting"). Fix ≤ 5 lines. |
| medium | The cause is in one file, but the issue describes higher-level behaviour (a statement, an import), so the solver must trace through 2–3 packages. Fix ≤ 10 lines. |
| hard | Needs an invariant that spans ≥ 2 files or packages. The issue describes a downstream symptom, and the first obvious suspect is not the cause. |

### Per-bug directory format (exact; task E.3 parses it)

- `issue.md` — first line `# <title>`, a blank line, then the user's report. Never mentions file names, functions
  or tests for medium and hard bugs.
- `bug.patch` — unified diff produced by `git diff --relative` inside `evals/fixtures/ledger`; paths look like
  `a/money/money.go`; applied in a copy of the fixture with `git apply -p1 bug.patch`.
- `fix.patch` — the reference fix, relative to the bugged tree, same format. (Normally the exact reverse of
  `bug.patch`.)
- `hidden_test.go` — one external test file (`package <pkg>_test`); every test function name starts with `TestHidden`.
- `meta.yaml` — the canonical format defined in task E.3 (`docs/impl/tasks/E.3-fixture-loader.md`, "On-disk
  format"). Exactly these keys, top-level keys unindented, lists as block lists (the check script reads the
  `test_package:` line with `sed`):

```yaml
id: money-split-remainder
difficulty: easy
title: Splitting an amount loses a cent  # quote it ("...") if it contains ": "
fixture: ledger
test_package: money
hidden_tests:
  - TestHiddenSplitRemainder
timeout: 2m
fix_files:
  - money/money.go
```

`id` equals the directory name and matches `^[a-z0-9][a-z0-9-]*$`. `test_package` is the fixture directory the
hidden test belongs to; the test is always copied to `<test_package>/adlc_hidden_test.go`. `hidden_tests` lists
every top-level test function in `hidden_test.go` (each starts with `TestHidden`). `title` is the issue title
without `# `. `fix_files` lists the files the reference fix changes (the "files:" of each bug below).
Use `timeout: 2m` for every bug.

### Creating a bug (do this for each)

```bash
cd evals/fixtures/ledger
# 1. introduce the bug by editing the files listed for it below
mkdir -p ../../suites/go-bugs/<id>
git diff --relative -- . > ../../suites/go-bugs/<id>/bug.patch
git diff --relative -R -- . > ../../suites/go-bugs/<id>/fix.patch
git checkout -- .                 # back to the clean fixture
cd ../../..
# 2. write issue.md, hidden_test.go, meta.yaml
evals/scripts/check-bug.sh <id>   # all six lines must say ok
```

### `evals/scripts/check-bug.sh` (exact)

```bash
#!/usr/bin/env bash
# Checks one seeded bug: usage: evals/scripts/check-bug.sh <bug-id>
set -euo pipefail
id="$1"
root="$(git rev-parse --show-toplevel)"
bug="$root/evals/suites/go-bugs/$id"
work="$(mktemp -d)"
cp -R "$root/evals/fixtures/ledger/." "$work/"
pkgdir="$(sed -n 's/^test_package: *//p' "$bug/meta.yaml")"
[ -n "$pkgdir" ] || { echo "$id: meta.yaml has no test_package line"; exit 1; }
dest="$pkgdir/adlc_hidden_test.go"
pkg="./$pkgdir"
run='^TestHidden
cd "$work"
step() { printf '%-40s' "$id: $1"; }
step "clean: hidden test passes"
cp "$bug/hidden_test.go" "$dest"
go test -count=1 -run "$run" "$pkg" >/dev/null && echo ok || { echo FAIL; exit 1; }
rm -f -- "$dest"
step "bug.patch applies"
git apply -p1 "$bug/bug.patch" && echo ok
step "bugged: vet + visible tests pass"
{ go vet ./... && go test -count=1 ./...; } >/dev/null 2>&1 && echo ok || { echo FAIL; exit 1; }
step "bugged: hidden test fails"
cp "$bug/hidden_test.go" "$dest"
if go test -count=1 -run "$run" "$pkg" >/dev/null 2>&1; then echo FAIL; exit 1; else echo ok; fi
step "fix.patch applies"
git apply -p1 "$bug/fix.patch" && echo ok
step "fixed: all tests pass"
go test -count=1 ./... >/dev/null && echo ok || { echo FAIL; exit 1; }
```

(The temporary directory is left in place on purpose; it is under the system temp directory.)

Makefile:

```make
bugs-check: ## Check every seeded bug (bug applies, hidden test fails, fix works)
	@for d in evals/suites/go-bugs/*/; do evals/scripts/check-bug.sh "$$(basename "$$d")" || exit 1; done
```

Add to `.PHONY`. Do **not** add it to `ci:` (it runs `go test` about 40 times); it is the quick tool for
writing bugs. Task E.3 adds the Go self-check and the CI job that runs on every PR.

## Bugs

For each bug: what to change to introduce it, the issue text, the hidden test, and the reference fix.
Write `issue.md` with exactly the title and text given (you may fix typos only).

### Easy

**1. `money-split-remainder`** — files: `money/money.go`
- Bug: in `Split`, drop the remainder distribution so every part is `a.Minor / n`.
- Issue: `# Splitting an amount loses a cent` — "When I split 10.00 EUR three ways with `Split` I get 3.33, 3.33 and
  3.33. That's 9.99 — a cent goes missing. The parts should add up to the original amount."
- Hidden test (package `money`): `TestHiddenSplitRemainder` — 10.00 EUR / 3 → 3.34, 3.33, 3.33;
  -10.00 EUR / 3 → -3.34, -3.33, -3.33; 0.01 EUR / 4 → 0.01, 0.00, 0.00, 0.00; for n in 1..12 and amounts
  -1001..1001 step 7, parts sum to the amount and differ by at most 1.
- Fix: restore the remainder distribution.

**2. `money-negative-cents-format`** — files: `money/money.go`
- Bug: in `String`, format as `fmt.Sprintf("%d.%02d %s", a.Minor/100, abs(a.Minor%100), a.Currency)` without a
  separate sign, so -5 prints `0.05 EUR` (for -105 it still prints `-1.05 EUR`).
- Issue: `# Small negative amounts print as positive` — "A balance of minus five cents shows as `0.05 EUR`. Bigger
  negative amounts like -1.05 print correctly."
- Hidden test: `TestHiddenStringNegativeCents` — `New(-5,"EUR").String() == "-0.05 EUR"`, `New(-99,...)` →
  `-0.99 EUR`; for every minor value -250..250, `Parse(a.String())` returns `a`.
- Fix: compute the sign separately and format the absolute value.

**3. `report-csv-comma`** — files: `report/report.go`
- Bug: `csvField` quotes fields only when they contain `"`, `\r` or `\n` (drop the comma check).
- Issue: `# CSV export breaks when a description has a comma` — "I exported my statement to CSV. The line
  'Rent, March' ends up split over two columns in my spreadsheet and everything after it is shifted."
- Hidden test: `TestHiddenCSVComma` — a statement with memo `Rent, March`; `WriteCSV` output read back with
  `encoding/csv` has 5 fields on every row and the memo field equals `Rent, March`.
- Fix: add `,` to the characters that force quoting.

### Medium

**4. `ledger-balance-asof`** — files: `ledger/ledger.go`
- Bug: in `Balance`, include entries with `e.Date.Before(asOf)` instead of `!e.Date.After(asOf)`.
- Issue: `# Month-end statement misses payments made on the last day` — "My statement for March doesn't include the
  payment I made on March 31 in the closing balance, but the payment is listed in the lines. So the running balance
  and the closing balance don't agree."
- Hidden test (package `ledger`): `TestHiddenBalanceIncludesAsOfDay` — entry on 2026-03-31;
  `Balance(acc, 2026-03-31)` includes it; `Statement` for 2026-03-01..2026-03-31 has `Closing` equal to the last
  line's `Balance`.
- Fix: inclusive comparison.

**5. `fx-rate-between-dates`** — files: `fx/fx.go`
- Bug: in `RateOn`, use the first rate whose date is on or after `date` (and the last rate if none is), instead of
  the latest rate on or before `date`. Exact rate dates still work.
- Issue: `# Conversions use a future exchange rate` — "We publish EUR→USD rates on the 10th and the 20th. A payment
  converted on the 15th used the rate from the 20th. It should use the rate that was in force on the 15th."
- Hidden test: `TestHiddenRateBetweenDates` — rates 1.10 on 2026-03-10 and 1.20 on 2026-03-20;
  `RateOn(EUR, USD, 2026-03-15) == 1_100_000`; `Convert(100.00 EUR, USD, 2026-03-15) == 110.00 USD`;
  `RateOn(..., 2026-03-25) == 1_200_000`; before 2026-03-10 → `ErrNoRate`.
- Fix: search for the latest rate with `Date <= date`.

**6. `importcsv-parentheses`** — files: `importcsv/importcsv.go`
- Bug: after `money.Parse`, write `if neg { amt.Neg() }` — the result of `Neg` is discarded, so the sign is lost.
- Issue: `# Card payments imported as deposits` — "Our bank shows outgoing payments in brackets, like (12.50). After
  importing the CSV, those payments appear as money coming in, so the balance goes up instead of down."
- Hidden test: `TestHiddenParenthesesAreNegative` — rows with `(12.50)` and `(1,234.56)` parse to -12.50 and
  -1234.56; `ToEntries` posts them as credits to the bank account.
- Fix: `amt = amt.Neg()`.

**7. `ledger-trial-balance-currencies`** — files: `ledger/ledger.go`
- Bug: build `Totals` by walking the account-sorted `Lines` and starting a new `CurrencyTotal` whenever the currency
  differs from the previous line's (instead of grouping by currency and sorting). Interleaved currencies produce
  duplicate rows; compute `Balanced` per row, so it becomes false.
- Issue: `# Trial balance shows EUR twice and says the books don't balance` — "Since we opened a USD account, the
  trial balance lists EUR totals twice and reports the books as unbalanced. Every entry we post is balanced."
- Hidden test: `TestHiddenTrialBalanceMultiCurrency` — accounts `assets:a` (EUR), `assets:b` (USD), `equity:a`
  (EUR), `equity:b` (USD) (account IDs chosen so currencies interleave when sorted); one balanced entry per currency;
  `TrialBalance` has exactly 2 totals (EUR, USD), each balanced, and `Balanced` is true.
- Fix: group by currency, then sort by currency.

### Hard

**8. `statement-opening-sign`** — files: `ledger/statement.go`
- Bug: multiply line amounts and `Closing` by `Kind.Normal()`, but not `Opening`. The running balance starts from the
  wrong sign for credit-normal accounts that had activity before `From`.
- Issue: `# Customer statements: running balance never reaches the closing balance` — "On statements for customer
  deposit accounts the opening balance is negative and the running balance at the bottom doesn't match the closing
  balance. Statements for our bank account look fine."
- Hidden test (package `ledger`): `TestHiddenLiabilityStatementOpening` — liability account
  `liabilities:customer:1`; deposit of 100.00 before `From` (bank +100.00, customer -100.00) and 50.00 inside the
  period; `Opening` = 100.00, last line `Balance` = 150.00 = `Closing`.
- Fix: apply `Normal()` to `Opening` too.

**9. `statement-fx-opening-date`** — files: `ledger/statement.go`
- Bug: for converted statements, convert `Opening` at the rate on `To` instead of `From - 1 day`.
- Issue: `# USD statement: February doesn't start where January ended` — "We send customers statements in USD for
  their EUR accounts. January's closing balance is not the same as February's opening balance, even though nothing
  happened in between."
- Hidden test: `TestHiddenConvertedOpeningMatchesPreviousClosing` — rates EUR→USD 1.10 from 2026-01-01 and 1.20 from
  2026-02-01; asset account in EUR with entries in January; January statement (01-01..01-31) and February statement
  (02-01..02-28), both in USD; `feb.Opening == jan.Closing`.
- Fix: use `from.AddDate(0, 0, -1)` for the opening conversion.

**10. `journal-load-reversed`** — files: `journal/store.go`
- Bug: `Load` restores entries but does not rebuild the `reversed` map, so entries reversed before saving can be
  reversed again.
- Issue: `# Refund reversed twice` — "A customer's balance is off by exactly one refund. In the journal there are two
  separate reversals of entry #7. The second one should have been rejected — reversing the same entry twice is
  supposed to fail."
- Hidden test (package `journal`): `TestHiddenLoadKeepsReversals` — post an entry, reverse it,
  `Save`, `Load`; reversing the original again returns `ErrAlreadyReversed`; reversing the reversal entry succeeds once.
- Fix: rebuild `reversed` from each loaded entry's `Reverses` field.

## Steps

1. Branch from latest main. Create `evals/suites/go.mod` and `evals/scripts/check-bug.sh` (`chmod +x`).
2. For bug 1: write `hidden_test.go`, run it against the clean fixture (it must pass), then create the bug and
   patches as in "Creating a bug", write `issue.md` and `meta.yaml`, run `check-bug.sh`. Commit nothing yet.
3. Repeat for bugs 2–10. If a visible test fails with a bug applied, the visible test covers a reserved behaviour:
   report it rather than weakening the test (see `AGENTS.md`).
4. Add `bugs-check` to the Makefile (not to `ci`). Run `make bugs-check`.
5. Write `evals/suites/README.md` (format, difficulty definitions, how to add a bug).
6. `make ci`.

## Tests

The tests of this task are the ten hidden tests and `make bugs-check`, which checks for every bug: hidden test
passes on the clean fixture; bug applies; with the bug, `go vet` and all visible tests pass; with the bug, the
hidden test fails; fix applies; with the fix, all tests pass.

## Verify

```bash
make bugs-check      # 60 lines ending in "ok"
make ci
git status --short evals/fixtures/   # nothing: the fixture itself is unchanged
```

## Out of scope

- The Go loader for this format (E.3), running agents (1.8), grading (1.9).
- More than 10 bugs, other fixtures.

## Pitfalls

- **`evals/suites/go.mod` is required.** Without it, the main module would treat each bug directory as a Go package
  and `go vet ./...` at the repo root would fail on `hidden_test.go`. A nested `go.mod` makes the Go tool skip the
  whole tree. Check with `go list ./... | grep -c suites` → `0`.
- Generate patches with `--relative` from inside the fixture, or the paths will start with `evals/fixtures/ledger/`.
- Run `git checkout -- .` in the fixture after creating each patch; the committed fixture must stay bug-free.
- Issue texts for medium and hard bugs must not name files, functions or tests.
- A hidden test must fail because of the bug, not because it does not compile: it may only use the fixture's public API.
- `go vet` must still pass with the bug applied (for bug 6, check that vet does not flag the discarded `Neg` result;
  if it does, report it).

## Done checklist

- [ ] 10 bug directories, each with the 5 files in the exact format
- [ ] `evals/suites/go.mod` exists; `go list ./... | grep -c suites` prints 0
- [ ] `make bugs-check` passes (it is not part of `make ci`)
- [ ] The fixture under `evals/fixtures/ledger` is unchanged
- [ ] `make ci` passes
