package ledger

import (
	"errors"
	"testing"

	"example.com/ledger/account"
	"example.com/ledger/money"
)

// checkLines compares statement lines with the expected amounts and running balances.
func checkLines(t *testing.T, got []Line, want []Line) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.EntryID != w.EntryID || !g.Date.Equal(w.Date) || g.Memo != w.Memo || g.Amount != w.Amount || g.Balance != w.Balance {
			t.Errorf("line %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestStatementAssetAccount(t *testing.T) {
	for _, in := range []money.Currency{"", "EUR"} {
		t.Run("in="+string(in), func(t *testing.T) {
			l := newLedger(t)
			transfer(t, l, day(2026, 1, 5), "opening sale", "assets:bank", "income:sales", 10000)
			transfer(t, l, day(2026, 3, 5), "salary", "assets:bank", "income:sales", 5000)
			transfer(t, l, day(2026, 3, 10), "rent", "expenses:rent", "assets:bank", 3000)
			transfer(t, l, day(2026, 4, 2), "after period", "assets:bank", "income:sales", 1000)

			st, err := l.Statement("assets:bank", day(2026, 3, 1), day(2026, 3, 31), in)
			if err != nil {
				t.Fatalf("Statement: %v", err)
			}
			if st.Currency != "EUR" || st.Account.ID != "assets:bank" {
				t.Errorf("Currency = %q, Account = %q; want EUR, assets:bank", st.Currency, st.Account.ID)
			}
			if want := money.New(10000, "EUR"); st.Opening != want {
				t.Errorf("Opening = %+v, want %+v", st.Opening, want)
			}
			if want := money.New(12000, "EUR"); st.Closing != want {
				t.Errorf("Closing = %+v, want %+v", st.Closing, want)
			}
			checkLines(t, st.Lines, []Line{
				{EntryID: 2, Date: day(2026, 3, 5), Memo: "salary", Amount: money.New(5000, "EUR"), Balance: money.New(15000, "EUR")},
				{EntryID: 3, Date: day(2026, 3, 10), Memo: "rent", Amount: money.New(-3000, "EUR"), Balance: money.New(12000, "EUR")},
			})
		})
	}
}

func TestStatementLiabilityAccountSigns(t *testing.T) {
	l := newLedger(t)
	transfer(t, l, day(2026, 3, 5), "deposit", "assets:bank", "liabilities:customer:1", 5000)
	transfer(t, l, day(2026, 3, 12), "withdrawal", "liabilities:customer:1", "assets:bank", 2000)

	st, err := l.Statement("liabilities:customer:1", day(2026, 3, 1), day(2026, 3, 31), "")
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	if want := money.New(0, "EUR"); st.Opening != want {
		t.Errorf("Opening = %+v, want %+v", st.Opening, want)
	}
	if want := money.New(3000, "EUR"); st.Closing != want {
		t.Errorf("Closing = %+v, want owed amount %+v", st.Closing, want)
	}
	checkLines(t, st.Lines, []Line{
		{EntryID: 1, Date: day(2026, 3, 5), Memo: "deposit", Amount: money.New(5000, "EUR"), Balance: money.New(5000, "EUR")},
		{EntryID: 2, Date: day(2026, 3, 12), Memo: "withdrawal", Amount: money.New(-2000, "EUR"), Balance: money.New(3000, "EUR")},
	})
}

func TestStatementOrdersLinesByDateThenID(t *testing.T) {
	l := newLedger(t)
	transfer(t, l, day(2026, 3, 12), "late", "assets:bank", "income:sales", 100)
	transfer(t, l, day(2026, 3, 5), "early", "assets:bank", "income:sales", 200)
	transfer(t, l, day(2026, 3, 5), "same day", "assets:bank", "income:sales", 300)
	transfer(t, l, day(2026, 3, 1), "first day", "assets:bank", "income:sales", 400)

	st, err := l.Statement("assets:bank", day(2026, 3, 1), day(2026, 3, 31), "")
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	var ids []int
	for _, line := range st.Lines {
		ids = append(ids, line.EntryID)
	}
	want := []int{4, 2, 3, 1}
	if len(ids) != len(want) {
		t.Fatalf("line IDs = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("line IDs = %v, want %v", ids, want)
		}
	}
	if want := money.New(1000, "EUR"); st.Closing != want {
		t.Errorf("Closing = %+v, want %+v", st.Closing, want)
	}
}

func TestStatementEmptyPeriod(t *testing.T) {
	l := newLedger(t)
	transfer(t, l, day(2026, 1, 5), "sale", "assets:bank", "income:sales", 10000)

	st, err := l.Statement("assets:bank", day(2026, 3, 1), day(2026, 3, 31), "")
	if err != nil {
		t.Fatalf("Statement: %v", err)
	}
	if len(st.Lines) != 0 {
		t.Errorf("Lines = %+v, want none", st.Lines)
	}
	if st.Opening != money.New(10000, "EUR") || st.Closing != st.Opening {
		t.Errorf("Opening %+v and Closing %+v, want both 100.00 EUR", st.Opening, st.Closing)
	}
}

func TestStatementRejectsUnknownAccount(t *testing.T) {
	_, err := newLedger(t).Statement("assets:ghost", day(2026, 3, 1), day(2026, 3, 31), "")
	if !errors.Is(err, account.ErrNotFound) {
		t.Errorf("Statement of unknown account error = %v, want account.ErrNotFound", err)
	}
}
