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

func TestHiddenLiabilityStatementOpening(t *testing.T) {
	reg := account.NewRegistry()
	for _, a := range []account.Account{
		{ID: "assets:bank", Name: "Bank", Kind: account.Asset, Currency: "EUR"},
		{ID: "liabilities:customer:1", Name: "Customer 1 deposit", Kind: account.Liability, Currency: "EUR"},
	} {
		if err := reg.Open(a); err != nil {
			t.Fatalf("Open %s: %v", a.ID, err)
		}
	}
	j := journal.New(reg)
	deposit := func(date time.Time, memo string, minor int64) {
		t.Helper()
		_, err := j.Post(date, memo,
			journal.Posting{Account: "assets:bank", Amount: money.New(minor, "EUR")},
			journal.Posting{Account: "liabilities:customer:1", Amount: money.New(-minor, "EUR")},
		)
		if err != nil {
			t.Fatalf("Post %s: %v", memo, err)
		}
	}
	deposit(hiddenDay(2026, time.February, 15), "first deposit", 10000)
	deposit(hiddenDay(2026, time.March, 10), "second deposit", 5000)
	l := &ledger.Ledger{Accounts: reg, Journal: j, FX: fx.NewTable()}

	st, err := l.Statement("liabilities:customer:1", hiddenDay(2026, time.March, 1), hiddenDay(2026, time.March, 31), "")
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	if want := money.New(10000, "EUR"); st.Opening != want {
		t.Errorf("Opening = %v, want %v", st.Opening, want)
	}
	if len(st.Lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(st.Lines))
	}
	want := money.New(15000, "EUR")
	if got := st.Lines[0].Balance; got != want {
		t.Errorf("last running balance = %v, want %v", got, want)
	}
	if st.Closing != want {
		t.Errorf("Closing = %v, want %v", st.Closing, want)
	}
}
