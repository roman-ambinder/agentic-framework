package journal_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/journal"
	"example.com/ledger/money"
)

func TestHiddenLoadKeepsReversals(t *testing.T) {
	reg := account.NewRegistry()
	for _, a := range []account.Account{
		{ID: "assets:bank", Name: "Bank", Kind: account.Asset, Currency: "EUR"},
		{ID: "income:sales", Name: "Sales", Kind: account.Income, Currency: "EUR"},
	} {
		if err := reg.Open(a); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	date := time.Date(2026, time.March, 5, 0, 0, 0, 0, time.UTC)
	j := journal.New(reg)
	orig, err := j.Post(date, "Sale",
		journal.Posting{Account: "assets:bank", Amount: money.New(1000, "EUR")},
		journal.Posting{Account: "income:sales", Amount: money.New(-1000, "EUR")},
	)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	rev, err := j.Reverse(orig.ID, date)
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	var buf bytes.Buffer
	if err := j.Save(&buf); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := journal.Load(&buf, reg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err := loaded.Reverse(orig.ID, date); !errors.Is(err, journal.ErrAlreadyReversed) {
		t.Errorf("second reversal of #%d: err = %v, want ErrAlreadyReversed", orig.ID, err)
	}
	if got := len(loaded.Entries()); got != 2 {
		t.Errorf("journal has %d entries after the refused reversal, want 2", got)
	}
	if _, err := loaded.Reverse(rev.ID, date); err != nil {
		t.Errorf("first reversal of the reversal #%d: %v", rev.ID, err)
	}
	if _, err := loaded.Reverse(rev.ID, date); !errors.Is(err, journal.ErrAlreadyReversed) {
		t.Errorf("second reversal of #%d: err = %v, want ErrAlreadyReversed", rev.ID, err)
	}
}
