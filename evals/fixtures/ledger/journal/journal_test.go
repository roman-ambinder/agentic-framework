package journal

import (
	"errors"
	"testing"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/money"
)

// testRegistry returns a registry with two EUR accounts, one expense account
// and one USD account.
func testRegistry(t *testing.T) *account.Registry {
	t.Helper()
	reg := account.NewRegistry()
	accounts := []account.Account{
		{ID: "assets:bank", Name: "Bank", Kind: account.Asset, Currency: "EUR"},
		{ID: "income:sales", Name: "Sales", Kind: account.Income, Currency: "EUR"},
		{ID: "expenses:fees", Name: "Fees", Kind: account.Expense, Currency: "EUR"},
		{ID: "assets:usd", Name: "USD bank", Kind: account.Asset, Currency: "USD"},
	}
	for _, a := range accounts {
		if err := reg.Open(a); err != nil {
			t.Fatalf("Open(%q): %v", a.ID, err)
		}
	}
	return reg
}

// march returns midnight UTC on the given day of March 2026.
func march(d int) time.Time {
	return time.Date(2026, time.March, d, 0, 0, 0, 0, time.UTC)
}

// sale returns two postings for a balanced EUR sale of the given minor units.
func sale(minor int64) []Posting {
	return []Posting{
		{Account: "assets:bank", Amount: money.New(minor, "EUR")},
		{Account: "income:sales", Amount: money.New(-minor, "EUR")},
	}
}

func mustPost(t *testing.T, j *Journal, date time.Time, memo string, postings ...Posting) Entry {
	t.Helper()
	e, err := j.Post(date, memo, postings...)
	if err != nil {
		t.Fatalf("Post(%q): %v", memo, err)
	}
	return e
}

func TestPostAssignsIDsFromOne(t *testing.T) {
	j := New(testRegistry(t))
	for want := 1; want <= 3; want++ {
		e := mustPost(t, j, march(want), "sale", sale(1000)...)
		if e.ID != want {
			t.Errorf("entry ID = %d, want %d", e.ID, want)
		}
	}
}

func TestPostRejectsTooFewPostings(t *testing.T) {
	j := New(testRegistry(t))
	_, err := j.Post(march(1), "one leg", Posting{Account: "assets:bank", Amount: money.New(0, "EUR")})
	if !errors.Is(err, ErrTooFewPostings) {
		t.Errorf("error = %v, want ErrTooFewPostings", err)
	}
	if _, err := j.Post(march(1), "none"); !errors.Is(err, ErrTooFewPostings) {
		t.Errorf("no postings error = %v, want ErrTooFewPostings", err)
	}
}

func TestPostRejectsUnbalanced(t *testing.T) {
	j := New(testRegistry(t))
	_, err := j.Post(march(1), "off by one", Posting{Account: "assets:bank", Amount: money.New(1000, "EUR")}, Posting{Account: "income:sales", Amount: money.New(-999, "EUR")})
	if !errors.Is(err, ErrUnbalanced) {
		t.Errorf("error = %v, want ErrUnbalanced", err)
	}
	if got := j.Entries(); len(got) != 0 {
		t.Errorf("rejected entry was stored: %+v", got)
	}
}

func TestPostRejectsUnknownAccount(t *testing.T) {
	j := New(testRegistry(t))
	_, err := j.Post(march(1), "ghost", Posting{Account: "assets:ghost", Amount: money.New(100, "EUR")}, Posting{Account: "income:sales", Amount: money.New(-100, "EUR")})
	if !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("error = %v, want ErrUnknownAccount", err)
	}
}

func TestPostRejectsClosedAccount(t *testing.T) {
	reg := testRegistry(t)
	if err := reg.Close("expenses:fees"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	j := New(reg)
	_, err := j.Post(march(1), "fee", Posting{Account: "expenses:fees", Amount: money.New(100, "EUR")}, Posting{Account: "assets:bank", Amount: money.New(-100, "EUR")})
	if !errors.Is(err, ErrClosedAccount) {
		t.Errorf("error = %v, want ErrClosedAccount", err)
	}
}

func TestPostRejectsWrongCurrency(t *testing.T) {
	j := New(testRegistry(t))
	_, err := j.Post(march(1), "mixed", Posting{Account: "assets:usd", Amount: money.New(100, "EUR")}, Posting{Account: "income:sales", Amount: money.New(-100, "EUR")})
	if !errors.Is(err, ErrWrongCurrency) {
		t.Errorf("error = %v, want ErrWrongCurrency", err)
	}
}

func TestPostCopiesPostings(t *testing.T) {
	j := New(testRegistry(t))
	postings := sale(2500)
	e := mustPost(t, j, march(1), "sale", postings...)
	postings[0].Amount = money.New(1, "EUR")
	e.Postings[1].Amount = money.New(7, "EUR")
	stored, ok := j.Get(1)
	if !ok {
		t.Fatal("Get(1): not found")
	}
	if stored.Postings[0].Amount.Minor != 2500 || stored.Postings[1].Amount.Minor != -2500 {
		t.Errorf("stored postings changed through a copy: %+v", stored.Postings)
	}
}

func TestGetReturnsEntryOrFalse(t *testing.T) {
	j := New(testRegistry(t))
	mustPost(t, j, march(1), "sale", sale(500)...)
	got, ok := j.Get(1)
	if !ok || got.Memo != "sale" {
		t.Errorf("Get(1) = %+v, %v; want memo sale, true", got, ok)
	}
	for _, id := range []int{0, 2, -1} {
		if _, ok := j.Get(id); ok {
			t.Errorf("Get(%d) found an entry, want none", id)
		}
	}
}

func TestEntriesReturnsCopyInIDOrder(t *testing.T) {
	j := New(testRegistry(t))
	mustPost(t, j, march(1), "first", sale(100)...)
	mustPost(t, j, march(2), "second", sale(200)...)
	entries := j.Entries()
	if len(entries) != 2 || entries[0].Memo != "first" || entries[1].Memo != "second" {
		t.Fatalf("Entries = %+v, want first then second", entries)
	}
	entries[0].Memo = "changed"
	entries[0].Postings[0].Amount = money.New(1, "EUR")
	if got, _ := j.Get(1); got.Memo != "first" || got.Postings[0].Amount.Minor != 100 {
		t.Errorf("journal changed through Entries copy: %+v", got)
	}
}

func TestReverseNegatesPostings(t *testing.T) {
	j := New(testRegistry(t))
	mustPost(t, j, march(1), "sale", sale(1500)...)
	rev, err := j.Reverse(1, march(5))
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if rev.ID != 2 || rev.Reverses != 1 {
		t.Errorf("reversal ID = %d, Reverses = %d; want 2 and 1", rev.ID, rev.Reverses)
	}
	if !rev.Date.Equal(march(5)) {
		t.Errorf("reversal date = %v, want 2026-03-05", rev.Date)
	}
	if want := "Reversal of #1: sale"; rev.Memo != want {
		t.Errorf("reversal memo = %q, want %q", rev.Memo, want)
	}
	if rev.Postings[0].Amount.Minor != -1500 || rev.Postings[1].Amount.Minor != 1500 {
		t.Errorf("reversal postings = %+v, want negated amounts", rev.Postings)
	}
}

func TestReverseMissingEntry(t *testing.T) {
	j := New(testRegistry(t))
	if _, err := j.Reverse(3, march(1)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Reverse of missing entry error = %v, want ErrNotFound", err)
	}
}

func TestReverseTwiceFails(t *testing.T) {
	j := New(testRegistry(t))
	mustPost(t, j, march(1), "sale", sale(1000)...)
	if _, err := j.Reverse(1, march(2)); err != nil {
		t.Fatalf("first Reverse: %v", err)
	}
	if _, err := j.Reverse(1, march(3)); !errors.Is(err, ErrAlreadyReversed) {
		t.Errorf("second Reverse error = %v, want ErrAlreadyReversed", err)
	}
	if got := len(j.Entries()); got != 2 {
		t.Errorf("journal has %d entries after rejected reversal, want 2", got)
	}
}

func TestReversalCanBeReversedOnce(t *testing.T) {
	j := New(testRegistry(t))
	mustPost(t, j, march(1), "sale", sale(1000)...)
	if _, err := j.Reverse(1, march(2)); err != nil {
		t.Fatalf("Reverse of sale: %v", err)
	}
	again, err := j.Reverse(2, march(3))
	if err != nil {
		t.Fatalf("Reverse of reversal: %v", err)
	}
	if again.Postings[0].Amount.Minor != 1000 {
		t.Errorf("re-reversal postings = %+v, want the original signs", again.Postings)
	}
	if _, err := j.Reverse(2, march(4)); !errors.Is(err, ErrAlreadyReversed) {
		t.Errorf("third Reverse error = %v, want ErrAlreadyReversed", err)
	}
}
