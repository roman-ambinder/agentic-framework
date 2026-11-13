// Package ledger answers questions about posted entries: balances, trial
// balances and account statements.
package ledger

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/fx"
	"example.com/ledger/journal"
	"example.com/ledger/money"
)

// Ledger ties together the chart of accounts, the journal and the exchange rates.
type Ledger struct {
	Accounts *account.Registry
	Journal  *journal.Journal
	FX       *fx.Table
}

// TBLine is one account's totals in a trial balance.
type TBLine struct {
	Account string
	Debit   money.Amount // sum of positive postings
	Credit  money.Amount // sum of negative postings, as a positive amount
}

// CurrencyTotal is the debit and credit totals of one currency in a trial balance.
type CurrencyTotal struct {
	Currency money.Currency
	Debit    money.Amount
	Credit   money.Amount
}

// TrialBalance lists the totals of every account with postings, as of a date.
type TrialBalance struct {
	AsOf     time.Time
	Lines    []TBLine        // one per account with postings, sorted by account ID
	Totals   []CurrencyTotal // one per currency, sorted by currency
	Balanced bool            // true if Debit == Credit in every currency
}

// Balance is the sum of all postings to accountID in entries dated on or before asOf
// (inclusive), in the account's currency. Debit-positive (raw) sign.
func (l *Ledger) Balance(accountID string, asOf time.Time) (money.Amount, error) {
	acc, ok := l.Accounts.Get(accountID)
	if !ok {
		return money.Amount{}, fmt.Errorf("ledger: balance %q: %w", accountID, account.ErrNotFound)
	}
	total := money.New(0, acc.Currency)
	for _, e := range l.Journal.Entries() {
		if e.Date.After(asOf) {
			continue
		}
		sum, err := postingsTo(e.Postings, accountID, acc.Currency)
		if err != nil {
			return money.Amount{}, fmt.Errorf("ledger: balance %q: %w", accountID, err)
		}
		if err := addTo(&total, sum); err != nil {
			return money.Amount{}, fmt.Errorf("ledger: balance %q: %w", accountID, err)
		}
	}
	return total, nil
}

// TrialBalance totals each currency separately; amounts in different currencies are never added together.
func (l *Ledger) TrialBalance(asOf time.Time) (TrialBalance, error) {
	lines := make(map[string]*TBLine)
	totals := make(map[money.Currency]*CurrencyTotal)
	for _, e := range l.Journal.Entries() {
		if e.Date.After(asOf) {
			continue
		}
		for _, p := range e.Postings {
			if err := l.addToTrialBalance(lines, totals, p); err != nil {
				return TrialBalance{}, err
			}
		}
	}
	tb := TrialBalance{AsOf: asOf, Balanced: true}
	for _, id := range slices.Sorted(maps.Keys(lines)) {
		tb.Lines = append(tb.Lines, *lines[id])
	}
	for _, c := range slices.Sorted(maps.Keys(totals)) {
		t := totals[c]
		tb.Totals = append(tb.Totals, *t)
		if t.Debit != t.Credit {
			tb.Balanced = false
		}
	}
	return tb, nil
}

// addToTrialBalance adds one posting to the account line and the currency total it belongs to.
func (l *Ledger) addToTrialBalance(lines map[string]*TBLine, totals map[money.Currency]*CurrencyTotal, p journal.Posting) error {
	acc, ok := l.Accounts.Get(p.Account)
	if !ok {
		return fmt.Errorf("ledger: trial balance: %q: %w", p.Account, account.ErrNotFound)
	}
	line, ok := lines[p.Account]
	if !ok {
		line = &TBLine{Account: p.Account, Debit: money.New(0, acc.Currency), Credit: money.New(0, acc.Currency)}
		lines[p.Account] = line
	}
	total, ok := totals[acc.Currency]
	if !ok {
		total = &CurrencyTotal{Currency: acc.Currency, Debit: money.New(0, acc.Currency), Credit: money.New(0, acc.Currency)}
		totals[acc.Currency] = total
	}
	side, sum := &line.Debit, &total.Debit
	amount := p.Amount
	if amount.Minor < 0 {
		side, sum = &line.Credit, &total.Credit
		amount = amount.Neg()
	}
	if err := addTo(side, amount); err != nil {
		return fmt.Errorf("ledger: trial balance: %w", err)
	}
	return addTo(sum, amount)
}

// addTo adds a to *dst, keeping the currency of *dst.
func addTo(dst *money.Amount, a money.Amount) error {
	sum, err := dst.Add(a)
	if err != nil {
		return err
	}
	*dst = sum
	return nil
}

// postingsTo sums the postings to accountID among ps, in currency c.
func postingsTo(ps []journal.Posting, accountID string, c money.Currency) (money.Amount, error) {
	total := money.New(0, c)
	for _, p := range ps {
		if p.Account != accountID {
			continue
		}
		if err := addTo(&total, p.Amount); err != nil {
			return money.Amount{}, err
		}
	}
	return total, nil
}
