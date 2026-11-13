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

func TestHiddenBalanceIncludesAsOfDay(t *testing.T) {
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
	post := func(date time.Time, memo string, minor int64) {
		t.Helper()
		_, err := j.Post(date, memo,
			journal.Posting{Account: "assets:bank", Amount: money.New(minor, "EUR")},
			journal.Posting{Account: "income:sales", Amount: money.New(-minor, "EUR")},
		)
		if err != nil {
			t.Fatalf("Post %s: %v", memo, err)
		}
	}
	post(hiddenDay(2026, time.March, 10), "early", 10000)
	post(hiddenDay(2026, time.March, 31), "month end", 2500)
	l := &ledger.Ledger{Accounts: reg, Journal: j, FX: fx.NewTable()}

	got, err := l.Balance("assets:bank", hiddenDay(2026, time.March, 31))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if want := money.New(12500, "EUR"); got != want {
		t.Errorf("Balance on the entry date = %v, want %v", got, want)
	}

	st, err := l.Statement("assets:bank", hiddenDay(2026, time.March, 1), hiddenDay(2026, time.March, 31), "")
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	if len(st.Lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(st.Lines))
	}
	if last := st.Lines[len(st.Lines)-1].Balance; last != st.Closing {
		t.Errorf("last running balance %v differs from closing balance %v", last, st.Closing)
	}
	if want := money.New(12500, "EUR"); st.Closing != want {
		t.Errorf("Closing = %v, want %v", st.Closing, want)
	}
}
