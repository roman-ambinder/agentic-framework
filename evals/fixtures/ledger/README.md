# ledger

`ledger` is a small double-entry bookkeeping library for Go. It keeps a chart of
accounts, posts balanced journal entries, converts amounts with dated exchange
rates, and produces balances, trial balances, account statements and
text or CSV reports. It imports bank CSV exports. It uses only the standard
library and has no external dependencies.

Amounts are stored as integers in minor units (cents). Dates are day-precision
`time.Time` values in UTC. A positive posting is a debit, a negative posting is
a credit, and every entry must balance in each currency.

## Packages

| Package | Purpose |
|---------|---------|
| `money` | `Amount` values, parsing and formatting, rounding, splitting |
| `account` | the chart of accounts: kinds, currencies, open and closed accounts |
| `journal` | balanced entries, reversals, saving and loading as JSON |
| `fx` | dated exchange rates and currency conversion |
| `ledger` | balances, trial balances and account statements |
| `importcsv` | reading bank exports and posting them to the journal |
| `report` | fixed-width text and CSV statement output |

## Example

```go
package main

import (
	"fmt"
	"os"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/fx"
	"example.com/ledger/journal"
	"example.com/ledger/ledger"
	"example.com/ledger/money"
	"example.com/ledger/report"
)

func main() {
	reg := account.NewRegistry()
	must(reg.Open(account.Account{ID: "assets:bank", Name: "Bank", Kind: account.Asset, Currency: "EUR"}))
	must(reg.Open(account.Account{ID: "income:consulting", Name: "Consulting", Kind: account.Income, Currency: "EUR"}))

	j := journal.New(reg)
	march := func(d int) time.Time { return time.Date(2026, time.March, d, 0, 0, 0, 0, time.UTC) }

	_, err := j.Post(march(5), "Invoice 1042",
		journal.Posting{Account: "assets:bank", Amount: money.New(250000, "EUR")},
		journal.Posting{Account: "income:consulting", Amount: money.New(-250000, "EUR")},
	)
	must(err)

	l := &ledger.Ledger{Accounts: reg, Journal: j, FX: fx.NewTable()}
	st, err := l.Statement("assets:bank", march(1), march(31), "")
	must(err)
	must(report.WriteStatement(os.Stdout, st))
	fmt.Println("closing:", st.Closing)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
```

Entries are never edited. To correct a posted entry, call `Reverse`, which posts
a new entry with every amount negated. An entry can be reversed only once.

Journals are saved with `Save` and read back with `Load`, which checks every
entry against the registry again.
