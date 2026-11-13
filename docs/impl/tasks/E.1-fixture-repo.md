# E.1 — Fixture repository

Size: M · Depends on: 0.0 · Branch: `task/E.1-fixture-repo` · PR title: `E.1: ledger fixture repository for evals`

## Goal

After this task, `evals/fixtures/ledger` is a small, realistic, bug-free Go
project (a double-entry bookkeeping library) with a passing test suite. Task
E.2 seeds bugs into it; the coder agent (task 1.10) is scored on fixing them.
It is its own Go module, uses only the standard library, and is deterministic.

## Read first

- `AGENTS.md`
- `docs/impl/tasks/E.2-seeded-bugs.md` — **section "Bugs"**: the behaviours listed there must work correctly in
  your code, and must *not* be covered by your tests (see "Reserved behaviours" below)
- `docs/plans/2026-11-13-adlc-work-plan.md` §4 (eval track)

## Files

Create, under `evals/fixtures/ledger/`:

```
go.mod                     module example.com/ledger, go 1.26, no requires
README.md                  what the library does (short, written like a real project)
money/money.go             Currency, Amount, Parse, String, arithmetic, Split, MulRate
money/money_test.go
account/account.go         Kind, Account, Registry
account/account_test.go
journal/journal.go         Posting, Entry, Journal, Post, Reverse
journal/store.go           Save, Load (JSON)
journal/journal_test.go
journal/store_test.go
fx/fx.go                   Rate, Table, RateOn, Convert
fx/fx_test.go
ledger/ledger.go           Ledger, Balance, TrialBalance
ledger/statement.go        Statement
ledger/ledger_test.go
ledger/statement_test.go
importcsv/importcsv.go     bank CSV import
importcsv/importcsv_test.go
report/report.go           text and CSV statement output
report/report_test.go
```

Change (repo root):
- `Makefile` — target `fixtures-test`, added to `ci`
- `.github/workflows/ci.yml` — nothing to change if `make` targets are used; `test` job runs `make test-race`, so add
  a step `- name: fixtures\n  run: make fixtures-test` to the `test` job

Target size: 1,000–1,800 lines including tests.

## Design

All dates are `time.Time` values at midnight UTC (`time.Date(y, m, d, 0, 0, 0, 0, time.UTC)`). Every currency has
2 decimal places; amounts are stored in minor units (cents) as `int64`. Positive posting amounts are debits,
negative amounts are credits.

### `money`

```go
type Currency string // three upper-case ASCII letters, e.g. "EUR"

var (
	ErrInvalidAmount    = errors.New("money: invalid amount")
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
)

type Amount struct {
	Minor    int64
	Currency Currency
}

func New(minor int64, c Currency) Amount
// Parse accepts "<number> <CUR>": optional leading '-', digits, optional '.' and 1–2 digits.
// "12.34 EUR", "-0.50 USD", "12 EUR", "0.5 EUR" (= 50 minor). Anything else → ErrInvalidAmount (wrapped with the input).
func Parse(s string) (Amount, error)
// String is the inverse of Parse with exactly two decimals: "12.34 EUR", "-0.05 EUR", "0.00 EUR".
func (a Amount) String() string
func (a Amount) Add(b Amount) (Amount, error) // ErrCurrencyMismatch if currencies differ
func (a Amount) Sub(b Amount) (Amount, error)
func (a Amount) Neg() Amount
func (a Amount) IsZero() bool
// MulRate multiplies by basisPoints/10000, rounding half away from zero (2.5 → 3, -2.5 → -3).
func (a Amount) MulRate(basisPoints int64) Amount
// Split divides a into n parts (n >= 1) that differ by at most one minor unit and sum exactly to a.
// Larger parts come first: 10.00 / 3 → 3.34, 3.33, 3.33. -10.00 / 3 → -3.34, -3.33, -3.33. n < 1 → ErrInvalidAmount.
func (a Amount) Split(n int) ([]Amount, error)
// Sum adds amounts that all have currency c; empty input → zero of c.
func Sum(c Currency, as ...Amount) (Amount, error)
```

### `account`

```go
type Kind int
const (
	Asset Kind = iota + 1
	Liability
	Equity
	Income
	Expense
)
func (k Kind) String() string // "asset", "liability", "equity", "income", "expense"
// Normal is +1 for debit-normal kinds (Asset, Expense) and -1 for credit-normal kinds (Liability, Equity, Income).
func (k Kind) Normal() int64

type Account struct {
	ID       string // ^[a-z0-9][a-z0-9:-]{0,63}$, e.g. "assets:bank", "liabilities:customer:42"
	Name     string
	Kind     Kind
	Currency money.Currency
	Closed   bool
}

var (
	ErrInvalidAccount = errors.New("account: invalid account")
	ErrDuplicate      = errors.New("account: duplicate id")
	ErrNotFound       = errors.New("account: not found")
)

type Registry struct{ /* map + nothing else */ }
func NewRegistry() *Registry
func (r *Registry) Open(a Account) error   // validates ID pattern, Kind, Currency (3 upper letters); sets Closed=false
func (r *Registry) Get(id string) (Account, bool)
func (r *Registry) Close(id string) error  // ErrNotFound
func (r *Registry) List() []Account        // sorted by ID
```

### `journal`

```go
type Posting struct {
	Account string
	Amount  money.Amount // > 0 debit, < 0 credit
}

type Entry struct {
	ID       int
	Date     time.Time
	Memo     string
	Postings []Posting
	Reverses int // ID of the entry this one reverses; 0 if none
}

var (
	ErrUnbalanced      = errors.New("journal: entry does not balance")
	ErrTooFewPostings  = errors.New("journal: entry needs at least two postings")
	ErrUnknownAccount  = errors.New("journal: unknown account")
	ErrClosedAccount   = errors.New("journal: account is closed")
	ErrWrongCurrency   = errors.New("journal: posting currency differs from account currency")
	ErrNotFound        = errors.New("journal: entry not found")
	ErrAlreadyReversed = errors.New("journal: entry already reversed")
)

type Journal struct {
	reg      *account.Registry
	entries  []Entry      // in ID order; IDs start at 1, no gaps
	reversed map[int]int  // entry ID → ID of the entry that reversed it
}

func New(reg *account.Registry) *Journal
// Post validates and appends a new entry with the next ID. Rules, checked in this order:
// at least 2 postings; every account exists, is open, and has the posting's currency;
// for each currency the posting amounts sum to zero.
func (j *Journal) Post(date time.Time, memo string, postings ...Posting) (Entry, error)
func (j *Journal) Get(id int) (Entry, bool)
func (j *Journal) Entries() []Entry // a copy, in ID order
// Reverse posts a new entry on date with every posting negated, memo "Reversal of #<id>: <memo>",
// Reverses = id. ErrNotFound if id does not exist; ErrAlreadyReversed if it was reversed before.
// A reversal entry itself can be reversed once.
func (j *Journal) Reverse(id int, date time.Time) (Entry, error)
```

`journal/store.go`:

```go
// Save writes the journal as JSON: {"version":1,"entries":[...]} with dates as "2006-01-02"
// and amounts as strings in money.String format.
func (j *Journal) Save(w io.Writer) error
// Load reads what Save wrote, re-validates every entry against reg, and rebuilds all internal
// state, including which entries have been reversed (from each entry's Reverses field).
func Load(r io.Reader, reg *account.Registry) (*Journal, error)
```

### `fx`

```go
var ErrNoRate = errors.New("fx: no rate")

// Rate says 1 unit of From = Micros/1_000_000 units of To, valid from Date on.
type Rate struct {
	From, To money.Currency
	Date     time.Time
	Micros   int64 // > 0
}

type Table struct{ /* rates per (From, To), sorted by Date */ }
func NewTable() *Table
func (t *Table) Add(r Rate) error // Micros <= 0 → error; same (From, To, Date) replaces the old rate
// RateOn returns the rate in force on date: the rate with the latest Date that is <= date.
// Same currency → 1_000_000. If only To→From rates exist, use the inverse:
// round_half_away(1e12 / inverse.Micros). No rate on or before date → ErrNoRate.
func (t *Table) RateOn(from, to money.Currency, date time.Time) (int64, error)
// Convert converts a into currency to using RateOn(a.Currency, to, date):
// minor = round_half_away(a.Minor * micros / 1e6).
func (t *Table) Convert(a money.Amount, to money.Currency, date time.Time) (money.Amount, error)
```

Use `sort.Search` over the sorted dates for `RateOn`.

### `ledger`

```go
type Ledger struct {
	Accounts *account.Registry
	Journal  *journal.Journal
	FX       *fx.Table
}

// Balance is the sum of all postings to accountID in entries dated on or before asOf
// (inclusive), in the account's currency. Debit-positive (raw) sign.
func (l *Ledger) Balance(accountID string, asOf time.Time) (money.Amount, error)

type TBLine struct {
	Account string
	Debit   money.Amount // sum of positive postings
	Credit  money.Amount // sum of negative postings, as a positive amount
}
type CurrencyTotal struct {
	Currency money.Currency
	Debit    money.Amount
	Credit   money.Amount
}
type TrialBalance struct {
	AsOf     time.Time
	Lines    []TBLine        // one per account with postings, sorted by account ID
	Totals   []CurrencyTotal // one per currency, sorted by currency
	Balanced bool            // true if Debit == Credit in every currency
}
// TrialBalance totals each currency separately; amounts in different currencies are never added together.
func (l *Ledger) TrialBalance(asOf time.Time) (TrialBalance, error)
```

`ledger/statement.go`:

```go
type Line struct {
	EntryID int
	Date    time.Time
	Memo    string
	Amount  money.Amount // in the statement currency, in the account's normal sign
	Balance money.Amount // running balance after this line, same sign rule
}

type Statement struct {
	Account  account.Account
	From, To time.Time
	Currency money.Currency // statement currency
	Opening  money.Amount   // balance at the end of the day before From
	Lines    []Line         // entries dated From..To inclusive, ordered by (Date, EntryID)
	Closing  money.Amount   // balance at the end of To
}

// Statement builds an account statement for From..To.
// Sign: every amount is multiplied by the account kind's Normal() (credit-normal accounts show
// money they owe as positive). This applies to Opening, each Line, and Closing.
// Currency: if in == "" or equals the account currency, no conversion. Otherwise Opening is converted at
// the rate on From-1 day, each Line at the rate on its entry date, Closing at the rate on To.
// Running balance for converted statements: Opening + sum of converted line amounts so far.
func (l *Ledger) Statement(accountID string, from, to time.Time, in money.Currency) (Statement, error)
```

### `importcsv`

```go
// Txn is one line of a bank export.
type Txn struct {
	Date   time.Time
	Memo   string
	Amount money.Amount
}
var ErrBadLine = errors.New("importcsv: bad line")

// Parse reads CSV with header "date,description,amount,currency" (use encoding/csv).
// date: "2006-01-02" or "02/01/2006" (day first). amount: "1234.56", "1,234.56", "-1,234.56", or "(1,234.56)" for
// negative. Errors name the line number: fmt.Errorf("%w: line %d: %v", ErrBadLine, n, err).
func Parse(r io.Reader) ([]Txn, error)

// ToEntries turns each Txn into a two-posting entry: bank account gets Amount, counter account gets -Amount.
func ToEntries(j *journal.Journal, txns []Txn, bank, counter string) ([]journal.Entry, error)
```

Amount parsing: strip the parentheses (remembering the sign), remove thousands separators (`,`), then call
`money.Parse(number + " " + currency)`, then negate if it was in parentheses.

### `report`

```go
// WriteStatement writes a fixed-width text statement (account, period, opening, one line per Line, closing).
func WriteStatement(w io.Writer, s ledger.Statement) error
// WriteCSV writes "date,entry,memo,amount,balance" rows with a header.
// Implement field quoting yourself with csvField (do not use encoding/csv here): a field that contains a comma,
// a double quote, '\r' or '\n' is wrapped in double quotes, and each inner double quote is doubled.
func WriteCSV(w io.Writer, s ledger.Statement) error
func csvField(s string) string
```

### Reserved behaviours (implement correctly, do not test)

E.2 adds hidden tests for these behaviours. Your code must handle them correctly, but **your visible tests must not
exercise them**, otherwise a seeded bug would be caught by the visible suite:

1. `Split` where the amount does not divide evenly (test only even splits, e.g. 9.00 / 3).
2. `String` of negative amounts with absolute value below 1.00 (e.g. -0.05).
3. `csvField`/`WriteCSV` with memos containing commas (quotes and newlines may be tested).
4. `Balance` with an entry dated exactly on `asOf` (test dates strictly before and strictly after only).
5. `RateOn`/`Convert` on a date between two rate dates (test exact rate dates and "before first rate" only).
6. `importcsv` amounts in parentheses.
7. `TrialBalance` with more than one currency.
8. `Statement` opening balance for credit-normal accounts (test statements only for Asset accounts, or Liability
   accounts with no entries before `From`).
9. Converted statements (`in` different from the account currency).
10. `Load` followed by `Reverse` of an entry that was reversed before saving.

Test everything else: parsing errors, validation errors of every kind, ID assignment, reversals before save,
Save/Load round trip of entries, TrialBalance for one currency, statements for asset accounts, text report layout,
CSV header and quoting of quotes, interest-free arithmetic, etc. Aim for over 80% statement coverage per package.

### Makefile target

```make
fixtures-test: ## Test the eval fixture repositories
	cd evals/fixtures/ledger && go vet ./... && go test -count=1 ./...
```

Add `fixtures-test` to the `ci:` prerequisites and to `.PHONY`.

## Steps

1. Branch from latest main. Create `evals/fixtures/ledger/go.mod` (`module example.com/ledger`, `go 1.26`).
2. Implement and test packages in dependency order: `money` → `account` → `journal` → `fx` → `ledger` →
   `importcsv` → `report`. After each package: `cd evals/fixtures/ledger && go test ./<pkg>/`.
3. Write a short, realistic `README.md` (what the library does, a usage example). Do not mention evals or bugs.
4. Add the Makefile target and CI step.
5. Confirm isolation: from the repo root, `go list ./... | grep -c ledger` prints `0` (the nested module is not
   part of the main module).
6. `make fmt` does not touch the fixture (nested module); run `cd evals/fixtures/ledger && gofmt -l .` and fix any file it lists.
7. `make ci`.

## Tests

Per package, table-driven, named `Test<Func><Case>`, for example `TestParseRejectsThreeDecimals`,
`TestPostRejectsUnbalanced`, `TestReverseTwiceFails`, `TestRateOnExactDate`, `TestStatementAssetAccount`,
`TestWriteCSVQuotesQuotes`. Respect the reserved list above.

## Verify

```bash
cd evals/fixtures/ledger && go vet ./... && go test -count=1 -cover ./...
cd ../../.. && go list ./... | grep -c ledger    # prints 0
make fixtures-test
make ci
```

Expected: every package `ok` with coverage above 80%; `0`; CI target passes.

## Out of scope

- Seeding bugs, hidden tests, fixture metadata (E.2, E.3).
- A command-line program, persistence other than `Save`/`Load`, concurrency.
- Third-party dependencies of any kind.

## Pitfalls

- Integer division in Go truncates toward zero: `-5 / 100 == 0` and `-5 % 100 == -5`. Handle signs explicitly in
  `String`, `MulRate`, `Split` and `Convert`.
- `time.Parse("02/01/2006", ...)` is day-first; that is intended.
- Never iterate a map when producing ordered output (`List`, `TrialBalance`, reports): sort first.
- The fixture must not import anything from the main module, and the main module must not import the fixture.
- Keep the code idiomatic and realistic; the agent must read it like a real project.

## Done checklist

- [ ] All packages and functions as specified; standard library only
- [ ] Reserved behaviours implemented correctly and not covered by visible tests
- [ ] Coverage above 80% per package
- [ ] `make fixtures-test` part of `make ci`; `make ci` passes
