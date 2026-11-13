// Package report writes account statements as fixed-width text and as CSV.
package report

import (
	"fmt"
	"io"
	"strings"

	"example.com/ledger/ledger"
)

const dateLayout = "2006-01-02"

// rowFormat lays out the text statement: date, entry, memo, amount, balance.
const rowFormat = "%-10s  %-6s  %-30s  %14s  %14s\n"

// WriteStatement writes a fixed-width text statement (account, period, opening, one line per Line, closing).
func WriteStatement(w io.Writer, s ledger.Statement) error {
	ew := &errWriter{w: w}
	ew.printf("Account:  %s %s\n", s.Account.ID, s.Account.Name)
	ew.printf("Period:   %s to %s\n", s.From.Format(dateLayout), s.To.Format(dateLayout))
	ew.printf("Currency: %s\n\n", s.Currency)
	ew.printf(rowFormat, "Date", "Entry", "Memo", "Amount", "Balance")
	ew.printf(rowFormat, "", "", "Opening balance", "", s.Opening.String())
	for _, l := range s.Lines {
		ew.printf(rowFormat, l.Date.Format(dateLayout), fmt.Sprintf("#%d", l.EntryID), l.Memo, l.Amount.String(), l.Balance.String())
	}
	ew.printf(rowFormat, "", "", "Closing balance", "", s.Closing.String())
	return ew.err
}

// WriteCSV writes "date,entry,memo,amount,balance" rows with a header.
// Implement field quoting yourself with csvField (do not use encoding/csv here): a field that contains a comma,
// a double quote, '\r' or '\n' is wrapped in double quotes, and each inner double quote is doubled.
func WriteCSV(w io.Writer, s ledger.Statement) error {
	ew := &errWriter{w: w}
	ew.printf("date,entry,memo,amount,balance\n")
	for _, l := range s.Lines {
		ew.printf("%s,%d,%s,%s,%s\n", l.Date.Format(dateLayout), l.EntryID, csvField(l.Memo), l.Amount.String(), l.Balance.String())
	}
	return ew.err
}

// csvField quotes s if it holds a character that would break a CSV row.
func csvField(s string) string {
	if !strings.ContainsAny(s, ",\"\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// errWriter writes formatted text and remembers the first write error, so callers check once.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
