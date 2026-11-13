package ledger

import (
	"errors"
	"testing"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/fx"
	"example.com/ledger/journal"
	"example.com/ledger/money"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// newLedger returns a ledger with EUR accounts for a bank, a customer deposit
// account, sales, rent and capital.
func newLedger(t *testing.T) *Ledger {
	t.Helper()
	reg := account.NewRegistry()
	accounts := []account.Account{
		{ID: "assets:bank", Name: "Bank", Kind: account.Asset, Currency: "EUR"},
		{ID: "liabilities:customer:1", Name: "Customer 1", Kind: account.Liability, Currency: "EUR"},
		{ID: "income:sales", Name: "Sales", Kind: account.Income, Currency: "EUR"},
		{ID: "expenses:rent", Name: "Rent", Kind: account.Expense, Currency: "EUR"},
		{ID: "equity:capital", Name: "Capital", Kind: account.Equity, Currency: "EUR"},
	}
	for _, a := range accounts {
		if err := reg.Open(a); err != nil {
			t.Fatalf("Open(%q): %v", a.ID, err)
		}
	}
	return &Ledger{Accounts: reg, Journal: journal.New(reg), FX: fx.NewTable()}
}

// transfer posts a two-posting EUR entry that debits one account and credits another.
func transfer(t *testing.T, l *Ledger, date time.Time, memo, debit, credit string, minor int64) journal.Entry {
	t.Helper()
	e, err := l.Journal.Post(date, memo,
		journal.Posting{Account: debit, Amount: money.New(minor, "EUR")},
		journal.Posting{Account: credit, Amount: money.New(-minor, "EUR")},
	)
	if err != nil {
		t.Fatalf("Post(%q): %v", memo, err)
	}
	return e
}

func TestBalanceAccumulatesUpToAsOf(t *testing.T) {
	l := newLedger(t)
	transfer(t, l, day(2026, 1, 5), "sale", "assets:bank", "income:sales", 10000)
	transfer(t, l, day(2026, 2, 10), "sale", "assets:bank", "income:sales", 5000)
	transfer(t, l, day(2026, 3, 20), "sale", "assets:bank", "income:sales", 2500)

	tests := []struct {
		name string
		asOf time.Time
		want int64
	}{
		{name: "before any entry", asOf: day(2025, 12, 31), want: 0},
		{name: "after first entry", asOf: day(2026, 1, 31), want: 10000},
		{name: "between entries", asOf: day(2026, 3, 15), want: 15000},
		{name: "after last entry", asOf: day(2026, 12, 31), want: 17500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := l.Balance("assets:bank", tt.asOf)
			if err != nil {
				t.Fatalf("Balance: %v", err)
			}
			if want := money.New(tt.want, "EUR"); got != want {
				t.Errorf("Balance = %+v, want %+v", got, want)
			}
		})
	}
}

func TestBalanceIsRawDebitPositive(t *testing.T) {
	l := newLedger(t)
	transfer(t, l, day(2026, 1, 5), "deposit", "assets:bank", "liabilities:customer:1", 10000)
	got, err := l.Balance("liabilities:customer:1", day(2026, 1, 31))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if want := money.New(-10000, "EUR"); got != want {
		t.Errorf("Balance of credit account = %+v, want raw %+v", got, want)
	}
}

func TestBalanceUnknownAccount(t *testing.T) {
	_, err := newLedger(t).Balance("assets:ghost", day(2026, 1, 1))
	if !errors.Is(err, account.ErrNotFound) {
		t.Errorf("Balance of unknown account error = %v, want account.ErrNotFound", err)
	}
}

func TestTrialBalanceOneCurrency(t *testing.T) {
	l := newLedger(t)
	transfer(t, l, day(2026, 1, 5), "sale", "assets:bank", "income:sales", 10000)
	transfer(t, l, day(2026, 2, 10), "rent", "expenses:rent", "assets:bank", 4000)
	transfer(t, l, day(2026, 3, 20), "later sale", "assets:bank", "income:sales", 2500)

	tb, err := l.TrialBalance(day(2026, 3, 15))
	if err != nil {
		t.Fatalf("TrialBalance: %v", err)
	}
	if !tb.AsOf.Equal(day(2026, 3, 15)) {
		t.Errorf("AsOf = %v, want 2026-03-15", tb.AsOf)
	}
	wantLines := []TBLine{
		{Account: "assets:bank", Debit: money.New(10000, "EUR"), Credit: money.New(4000, "EUR")},
		{Account: "expenses:rent", Debit: money.New(4000, "EUR"), Credit: money.New(0, "EUR")},
		{Account: "income:sales", Debit: money.New(0, "EUR"), Credit: money.New(10000, "EUR")},
	}
	if len(tb.Lines) != len(wantLines) {
		t.Fatalf("got %d lines, want %d: %+v", len(tb.Lines), len(wantLines), tb.Lines)
	}
	for i, want := range wantLines {
		if tb.Lines[i] != want {
			t.Errorf("line %d = %+v, want %+v", i, tb.Lines[i], want)
		}
	}
	wantTotal := CurrencyTotal{Currency: "EUR", Debit: money.New(14000, "EUR"), Credit: money.New(14000, "EUR")}
	if len(tb.Totals) != 1 || tb.Totals[0] != wantTotal {
		t.Errorf("Totals = %+v, want [%+v]", tb.Totals, wantTotal)
	}
	if !tb.Balanced {
		t.Error("Balanced = false, want true")
	}
}

func TestTrialBalanceEmptyJournal(t *testing.T) {
	tb, err := newLedger(t).TrialBalance(day(2026, 1, 1))
	if err != nil {
		t.Fatalf("TrialBalance: %v", err)
	}
	if len(tb.Lines) != 0 || len(tb.Totals) != 0 || !tb.Balanced {
		t.Errorf("TrialBalance of empty journal = %+v, want no lines, no totals, balanced", tb)
	}
}
