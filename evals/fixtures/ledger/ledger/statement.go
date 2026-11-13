package ledger

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/fx"
	"example.com/ledger/journal"
	"example.com/ledger/money"
)

// Line is one entry's effect on an account, as shown on a statement.
type Line struct {
	EntryID int
	Date    time.Time
	Memo    string
	Amount  money.Amount // in the statement currency, in the account's normal sign
	Balance money.Amount // running balance after this line, same sign rule
}

// Statement is the activity of one account over a period.
type Statement struct {
	Account  account.Account
	From, To time.Time
	Currency money.Currency // statement currency
	Opening  money.Amount   // balance at the end of the day before From
	Lines    []Line         // entries dated From..To inclusive, ordered by (Date, EntryID)
	Closing  money.Amount   // balance at the end of To
}

// presenter turns raw (debit-positive) account amounts into statement amounts:
// it applies the account's normal sign and, when needed, converts the currency.
type presenter struct {
	sign    int64
	rates   *fx.Table
	convert bool
	in      money.Currency
}

// apply returns the statement amount for raw, using the rate on date when converting.
func (p presenter) apply(raw money.Amount, date time.Time) (money.Amount, error) {
	a := raw
	if p.sign < 0 {
		a = raw.Neg()
	}
	if !p.convert {
		return a, nil
	}
	return p.rates.Convert(a, p.in, date)
}

// Statement builds an account statement for From..To.
// Sign: every amount is multiplied by the account kind's Normal() (credit-normal accounts show
// money they owe as positive). This applies to Opening, each Line, and Closing.
// Currency: if in == "" or equals the account currency, no conversion. Otherwise Opening is converted at
// the rate on From-1 day, each Line at the rate on its entry date, Closing at the rate on To.
// Running balance for converted statements: Opening + sum of converted line amounts so far.
func (l *Ledger) Statement(accountID string, from, to time.Time, in money.Currency) (Statement, error) {
	acc, ok := l.Accounts.Get(accountID)
	if !ok {
		return Statement{}, fmt.Errorf("ledger: statement %q: %w", accountID, account.ErrNotFound)
	}
	p := presenter{sign: acc.Kind.Normal(), rates: l.FX}
	cur := acc.Currency
	if in != "" && in != acc.Currency {
		if l.FX == nil {
			return Statement{}, fmt.Errorf("ledger: statement %q: no exchange rates to convert to %s", accountID, in)
		}
		cur, p.convert, p.in = in, true, in
	}
	before := from.AddDate(0, 0, -1)
	openRaw, err := l.Balance(accountID, before)
	if err != nil {
		return Statement{}, err
	}
	opening, err := p.apply(openRaw, before)
	if err != nil {
		return Statement{}, fmt.Errorf("ledger: statement %q: opening: %w", accountID, err)
	}
	st := Statement{Account: acc, From: from, To: to, Currency: cur, Opening: opening}
	running := opening
	for _, e := range l.periodEntries(accountID, from, to) {
		line, err := statementLine(p, e, accountID, acc.Currency)
		if err != nil {
			return Statement{}, fmt.Errorf("ledger: statement %q: entry %d: %w", accountID, e.ID, err)
		}
		if err := addTo(&running, line.Amount); err != nil {
			return Statement{}, err
		}
		line.Balance = running
		st.Lines = append(st.Lines, line)
	}
	closeRaw, err := l.Balance(accountID, to)
	if err != nil {
		return Statement{}, err
	}
	st.Closing, err = p.apply(closeRaw, to)
	if err != nil {
		return Statement{}, fmt.Errorf("ledger: statement %q: closing: %w", accountID, err)
	}
	return st, nil
}

// statementLine builds the line for one entry. Its Balance is filled in by the caller.
func statementLine(p presenter, e journal.Entry, accountID string, c money.Currency) (Line, error) {
	raw, err := postingsTo(e.Postings, accountID, c)
	if err != nil {
		return Line{}, err
	}
	amount, err := p.apply(raw, e.Date)
	if err != nil {
		return Line{}, err
	}
	return Line{EntryID: e.ID, Date: e.Date, Memo: e.Memo, Amount: amount}, nil
}

// periodEntries returns the entries dated From..To inclusive that have postings to
// accountID, ordered by (Date, EntryID).
func (l *Ledger) periodEntries(accountID string, from, to time.Time) []journal.Entry {
	var out []journal.Entry
	for _, e := range l.Journal.Entries() {
		if e.Date.Before(from) || e.Date.After(to) || !touches(e, accountID) {
			continue
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b journal.Entry) int {
		return cmp.Or(a.Date.Compare(b.Date), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// touches reports whether the entry has any posting to accountID.
func touches(e journal.Entry, accountID string) bool {
	return slices.ContainsFunc(e.Postings, func(p journal.Posting) bool {
		return p.Account == accountID
	})
}
