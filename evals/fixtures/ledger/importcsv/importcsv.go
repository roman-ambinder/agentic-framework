// Package importcsv reads bank exports in CSV form and posts them to a journal.
package importcsv

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"example.com/ledger/journal"
	"example.com/ledger/money"
)

// Txn is one line of a bank export.
type Txn struct {
	Date   time.Time
	Memo   string
	Amount money.Amount
}

// ErrBadLine reports a line of a bank export that cannot be read.
var ErrBadLine = errors.New("importcsv: bad line")

const (
	isoLayout      = "2006-01-02"
	dayFirstLayout = "02/01/2006"
)

// header is the first line every export must have.
var header = []string{"date", "description", "amount", "currency"}

// Parse reads CSV with header "date,description,amount,currency" (use encoding/csv).
// date: "2006-01-02" or "02/01/2006" (day first). amount: "1234.56", "1,234.56", "-1,234.56", or "(1,234.56)" for
// negative. Errors name the line number: fmt.Errorf("%w: line %d: %v", ErrBadLine, n, err).
func Parse(r io.Reader) ([]Txn, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = len(header)
	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: line 1: %w", ErrBadLine, err)
	}
	if !slices.Equal(head, header) {
		return nil, fmt.Errorf("%w: line 1: header is %q, want %q", ErrBadLine, head, header)
	}
	var txns []Txn
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return txns, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBadLine, err)
		}
		line, _ := cr.FieldPos(0)
		txn, err := parseRecord(rec)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrBadLine, line, err)
		}
		txns = append(txns, txn)
	}
}

// parseRecord converts one CSV record, already checked to have four fields, into a Txn.
func parseRecord(rec []string) (Txn, error) {
	date, err := parseDate(rec[0])
	if err != nil {
		return Txn{}, err
	}
	amount, err := parseAmount(rec[2], rec[3])
	if err != nil {
		return Txn{}, err
	}
	return Txn{Date: date, Memo: strings.TrimSpace(rec[1]), Amount: amount}, nil
}

// parseDate accepts an ISO date or a day-first date.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{isoLayout, dayFirstLayout} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("date %q is not YYYY-MM-DD or DD/MM/YYYY", s)
}

// parseAmount reads a bank amount such as "-1,234.56" or "(12.50)". Brackets mean a negative amount.
func parseAmount(raw, currency string) (money.Amount, error) {
	s := strings.TrimSpace(raw)
	negative := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	if negative {
		s = s[1 : len(s)-1]
	}
	s = strings.ReplaceAll(s, ",", "")
	amt, err := money.Parse(s + " " + strings.TrimSpace(currency))
	if err != nil {
		return money.Amount{}, err
	}
	if negative {
		amt = amt.Neg()
	}
	return amt, nil
}

// ToEntries turns each Txn into a two-posting entry: bank account gets Amount, counter account gets -Amount.
// It posts the entries in order and stops at the first one the journal refuses; entries posted before it stay posted.
func ToEntries(j *journal.Journal, txns []Txn, bank, counter string) ([]journal.Entry, error) {
	entries := make([]journal.Entry, 0, len(txns))
	for i, t := range txns {
		e, err := j.Post(t.Date, t.Memo,
			journal.Posting{Account: bank, Amount: t.Amount},
			journal.Posting{Account: counter, Amount: t.Amount.Neg()},
		)
		if err != nil {
			return entries, fmt.Errorf("importcsv: transaction %d: %w", i+1, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}
