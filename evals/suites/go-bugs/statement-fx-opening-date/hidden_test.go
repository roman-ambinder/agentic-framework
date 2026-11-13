package ledger_test

import (
	"testing"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/fx"
	"example.com/ledger/journal"
	"example.com/ledger/ledger"
	"example.com/ledger/money"
)

func hiddenDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestHiddenConvertedOpeningMatchesPreviousClosing(t *testing.T) {
	reg := account.NewRegistry()
	for _, a := range []account.Account{
		{ID: "assets:bank", Name: "Bank", Kind: account.Asset, Currency: "EUR"},
		{ID: "income:sales", Name: "Sales", Kind: account.Income, Currency: "EUR"},
	} {
		if err := reg.Open(a); err != nil {
			t.Fatalf("Open %s: %v", a.ID, err)
		}
	}
	j := journal.New(reg)
	for _, e := range []struct {
		date  time.Time
		minor int64
	}{
		{hiddenDay(2026, time.January, 10), 10000},
		{hiddenDay(2026, time.January, 20), 5050},
	} {
		_, err := j.Post(e.date, "Sale",
			journal.Posting{Account: "assets:bank", Amount: money.New(e.minor, "EUR")},
			journal.Posting{Account: "income:sales", Amount: money.New(-e.minor, "EUR")},
		)
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
	}
	rates := fx.NewTable()
	for _, r := range []fx.Rate{
		{From: "EUR", To: "USD", Date: hiddenDay(2025, time.December, 1), Micros: 1_100_000},
		{From: "EUR", To: "USD", Date: hiddenDay(2026, time.February, 1), Micros: 1_200_000},
	} {
		if err := rates.Add(r); err != nil {
			t.Fatalf("Add rate: %v", err)
		}
	}
	l := &ledger.Ledger{Accounts: reg, Journal: j, FX: rates}

	jan, err := l.Statement("assets:bank", hiddenDay(2026, time.January, 1), hiddenDay(2026, time.January, 31), "USD")
	if err != nil {
		t.Fatalf("January statement: %v", err)
	}
	feb, err := l.Statement("assets:bank", hiddenDay(2026, time.February, 1), hiddenDay(2026, time.February, 28), "USD")
	if err != nil {
		t.Fatalf("February statement: %v", err)
	}
	if feb.Opening != jan.Closing {
		t.Errorf("February opening %v differs from January closing %v", feb.Opening, jan.Closing)
	}
}
