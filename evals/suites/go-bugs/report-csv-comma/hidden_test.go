package report_test

import (
	"bytes"
	"encoding/csv"
	"testing"
	"time"

	"example.com/ledger/ledger"
	"example.com/ledger/money"
	"example.com/ledger/report"
)

func TestHiddenCSVComma(t *testing.T) {
	day := time.Date(2026, time.March, 5, 0, 0, 0, 0, time.UTC)
	st := ledger.Statement{
		From:     time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC),
		To:       time.Date(2026, time.March, 31, 0, 0, 0, 0, time.UTC),
		Currency: "EUR",
		Opening:  money.New(0, "EUR"),
		Lines: []ledger.Line{
			{EntryID: 1, Date: day, Memo: "Rent, March", Amount: money.New(-80000, "EUR"), Balance: money.New(-80000, "EUR")},
			{EntryID: 2, Date: day, Memo: "Coffee", Amount: money.New(-350, "EUR"), Balance: money.New(-80350, "EUR")},
		},
		Closing: money.New(-80350, "EUR"),
	}
	var buf bytes.Buffer
	if err := report.WriteCSV(&buf, st); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("reading the CSV back: %v\n%s", err, buf.String())
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	for i, row := range rows {
		if len(row) != 5 {
			t.Errorf("row %d has %d fields, want 5: %q", i, len(row), row)
		}
	}
	if got := rows[1][2]; got != "Rent, March" {
		t.Errorf("memo = %q, want %q", got, "Rent, March")
	}
}
