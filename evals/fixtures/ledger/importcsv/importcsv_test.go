package importcsv

import (
	"errors"
	"strings"
	"testing"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/journal"
	"example.com/ledger/money"
)

// march returns midnight UTC on the given day of March 2026.
func march(d int) time.Time {
	return time.Date(2026, time.March, d, 0, 0, 0, 0, time.UTC)
}

func TestParseReadsRows(t *testing.T) {
	doc := `date,description,amount,currency
2026-03-05,Salary,1234.56,EUR
02/03/2026,Coffee shop,-3.50,EUR
2026-03-06,Grocer,"-1,234.56",EUR
`
	txns, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Txn{
		{Date: march(5), Memo: "Salary", Amount: money.New(123456, "EUR")},
		{Date: march(2), Memo: "Coffee shop", Amount: money.New(-350, "EUR")},
		{Date: march(6), Memo: "Grocer", Amount: money.New(-123456, "EUR")},
	}
	if len(txns) != len(want) {
		t.Fatalf("Parse returned %d transactions, want %d", len(txns), len(want))
	}
	for i := range want {
		g, w := txns[i], want[i]
		if !g.Date.Equal(w.Date) || g.Memo != w.Memo || g.Amount != w.Amount {
			t.Errorf("txn %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestParseHeaderOnlyIsEmpty(t *testing.T) {
	txns, err := Parse(strings.NewReader("date,description,amount,currency\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(txns) != 0 {
		t.Errorf("Parse returned %d transactions, want none", len(txns))
	}
}

func TestParseRejectsBadRows(t *testing.T) {
	const header = "date,description,amount,currency\n"
	tests := []struct {
		name     string
		doc      string
		wantLine string
	}{
		{name: "wrong header", doc: "date,memo,amount,currency\n", wantLine: "line 1"},
		{name: "empty input", doc: "", wantLine: "line 1"},
		{name: "bad date", doc: header + "2026-13-01,Rent,10.00,EUR\n", wantLine: "line 2"},
		{name: "three decimals", doc: header + "2026-03-01,Rent,10.005,EUR\n", wantLine: "line 2"},
		{name: "lower-case currency", doc: header + "2026-03-01,Rent,10.00,eur\n", wantLine: "line 2"},
		{name: "missing field", doc: header + "2026-03-01,Rent,10.00\n", wantLine: "line 2"},
		{name: "bad line after good", doc: header + "2026-03-01,Rent,10.00,EUR\n2026-03-02,Fee,x,EUR\n", wantLine: "line 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.doc))
			if !errors.Is(err, ErrBadLine) {
				t.Fatalf("Parse error = %v, want ErrBadLine", err)
			}
			if !strings.Contains(err.Error(), tt.wantLine) {
				t.Errorf("error %q does not name %s", err, tt.wantLine)
			}
		})
	}
}

func TestToEntriesPostsBothSides(t *testing.T) {
	reg := account.NewRegistry()
	for _, a := range []account.Account{
		{ID: "assets:bank", Kind: account.Asset, Currency: "EUR"},
		{ID: "expenses:card", Kind: account.Expense, Currency: "EUR"},
	} {
		if err := reg.Open(a); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	j := journal.New(reg)
	txns := []Txn{{Date: march(9), Memo: "Card", Amount: money.New(-1250, "EUR")}}

	entries, err := ToEntries(j, txns, "assets:bank", "expenses:card")
	if err != nil {
		t.Fatalf("ToEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("ToEntries returned %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.Memo != "Card" || !e.Date.Equal(march(9)) {
		t.Errorf("entry = %+v, want memo Card dated 2026-03-09", e)
	}
	bank, counter := e.Postings[0], e.Postings[1]
	if bank.Account != "assets:bank" || bank.Amount != money.New(-1250, "EUR") {
		t.Errorf("bank posting = %+v, want credit of 12.50", bank)
	}
	if counter.Account != "expenses:card" || counter.Amount != money.New(1250, "EUR") {
		t.Errorf("counter posting = %+v, want debit of 12.50", counter)
	}
	if got := j.Entries(); len(got) != 1 {
		t.Errorf("journal holds %d entries, want 1", len(got))
	}
}

func TestToEntriesStopsAtRefusedEntry(t *testing.T) {
	reg := account.NewRegistry()
	if err := reg.Open(account.Account{ID: "assets:bank", Kind: account.Asset, Currency: "EUR"}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	txns := []Txn{{Date: march(9), Memo: "Card", Amount: money.New(500, "EUR")}}

	entries, err := ToEntries(journal.New(reg), txns, "assets:bank", "expenses:unknown")
	if !errors.Is(err, journal.ErrUnknownAccount) {
		t.Errorf("ToEntries error = %v, want journal.ErrUnknownAccount", err)
	}
	if len(entries) != 0 {
		t.Errorf("ToEntries returned %d entries before the failure, want 0", len(entries))
	}
}
