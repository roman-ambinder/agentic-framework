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

func TestHiddenTrialBalanceMultiCurrency(t *testing.T) {
	reg := account.NewRegistry()
	for _, a := range []account.Account{
		{ID: "assets:a", Name: "Assets EUR", Kind: account.Asset, Currency: "EUR"},
		{ID: "assets:b", Name: "Assets USD", Kind: account.Asset, Currency: "USD"},
		{ID: "equity:a", Name: "Equity EUR", Kind: account.Equity, Currency: "EUR"},
		{ID: "equity:b", Name: "Equity USD", Kind: account.Equity, Currency: "USD"},
	} {
		if err := reg.Open(a); err != nil {
			t.Fatalf("Open %s: %v", a.ID, err)
		}
	}
	j := journal.New(reg)
	date := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	if _, err := j.Post(date, "EUR capital",
		journal.Posting{Account: "assets:a", Amount: money.New(10000, "EUR")},
		journal.Posting{Account: "equity:a", Amount: money.New(-10000, "EUR")},
	); err != nil {
		t.Fatalf("Post EUR: %v", err)
	}
	if _, err := j.Post(date, "USD capital",
		journal.Posting{Account: "assets:b", Amount: money.New(5000, "USD")},
		journal.Posting{Account: "equity:b", Amount: money.New(-5000, "USD")},
	); err != nil {
		t.Fatalf("Post USD: %v", err)
	}
	l := &ledger.Ledger{Accounts: reg, Journal: j, FX: fx.NewTable()}

	tb, err := l.TrialBalance(date)
	if err != nil {
		t.Fatalf("TrialBalance: %v", err)
	}
	if len(tb.Totals) != 2 {
		t.Fatalf("got %d currency totals, want 2: %+v", len(tb.Totals), tb.Totals)
	}
	want := []struct {
		cur   money.Currency
		minor int64
	}{{"EUR", 10000}, {"USD", 5000}}
	for i, w := range want {
		got := tb.Totals[i]
		if got.Currency != w.cur {
			t.Errorf("totals[%d] currency = %s, want %s", i, got.Currency, w.cur)
		}
		if got.Debit != money.New(w.minor, w.cur) || got.Credit != money.New(w.minor, w.cur) {
			t.Errorf("totals[%d] = debit %v credit %v, want %d each", i, got.Debit, got.Credit, w.minor)
		}
	}
	if !tb.Balanced {
		t.Error("Balanced = false, want true")
	}
}
