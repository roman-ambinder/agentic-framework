package importcsv_test

import (
	"strings"
	"testing"

	"example.com/ledger/account"
	"example.com/ledger/importcsv"
	"example.com/ledger/journal"
	"example.com/ledger/money"
)

func TestHiddenParenthesesAreNegative(t *testing.T) {
	const data = "date,description,amount,currency\n" +
		"2026-03-02,Card payment,(12.50),EUR\n" +
		"2026-03-03,Big payment,\"(1,234.56)\",EUR\n"
	txns, err := importcsv.Parse(strings.NewReader(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []int64{-1250, -123456}
	if len(txns) != len(want) {
		t.Fatalf("got %d transactions, want %d", len(txns), len(want))
	}
	for i, w := range want {
		if got := txns[i].Amount; got != money.New(w, "EUR") {
			t.Errorf("transaction %d amount = %v, want %v", i+1, got, money.New(w, "EUR"))
		}
	}

	reg := account.NewRegistry()
	for _, a := range []account.Account{
		{ID: "assets:bank", Name: "Bank", Kind: account.Asset, Currency: "EUR"},
		{ID: "expenses:misc", Name: "Misc", Kind: account.Expense, Currency: "EUR"},
	} {
		if err := reg.Open(a); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	j := journal.New(reg)
	entries, err := importcsv.ToEntries(j, txns, "assets:bank", "expenses:misc")
	if err != nil {
		t.Fatalf("ToEntries: %v", err)
	}
	for i, e := range entries {
		bank := e.Postings[0]
		if bank.Account != "assets:bank" || bank.Amount.Minor >= 0 {
			t.Errorf("entry %d: bank posting %+v, want a credit (negative amount)", i+1, bank)
		}
		if bank.Amount != money.New(want[i], "EUR") {
			t.Errorf("entry %d: bank amount %v, want %v", i+1, bank.Amount, money.New(want[i], "EUR"))
		}
	}
}
