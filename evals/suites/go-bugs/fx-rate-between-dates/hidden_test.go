package fx_test

import (
	"errors"
	"testing"
	"time"

	"example.com/ledger/fx"
	"example.com/ledger/money"
)

func hiddenMarch(d int) time.Time {
	return time.Date(2026, time.March, d, 0, 0, 0, 0, time.UTC)
}

func TestHiddenRateBetweenDates(t *testing.T) {
	tbl := fx.NewTable()
	for _, r := range []fx.Rate{
		{From: "EUR", To: "USD", Date: hiddenMarch(10), Micros: 1_100_000},
		{From: "EUR", To: "USD", Date: hiddenMarch(20), Micros: 1_200_000},
	} {
		if err := tbl.Add(r); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	t.Run("between two rates uses the earlier one", func(t *testing.T) {
		got, err := tbl.RateOn("EUR", "USD", hiddenMarch(15))
		if err != nil {
			t.Fatalf("RateOn: %v", err)
		}
		if got != 1_100_000 {
			t.Errorf("RateOn = %d, want 1100000", got)
		}
	})
	t.Run("convert between two rates", func(t *testing.T) {
		got, err := tbl.Convert(money.New(10000, "EUR"), "USD", hiddenMarch(15))
		if err != nil {
			t.Fatalf("Convert: %v", err)
		}
		if want := money.New(11000, "USD"); got != want {
			t.Errorf("Convert = %v, want %v", got, want)
		}
	})
	t.Run("after the last rate uses the last one", func(t *testing.T) {
		got, err := tbl.RateOn("EUR", "USD", hiddenMarch(25))
		if err != nil {
			t.Fatalf("RateOn: %v", err)
		}
		if got != 1_200_000 {
			t.Errorf("RateOn = %d, want 1200000", got)
		}
	})
	t.Run("before the first rate there is none", func(t *testing.T) {
		if _, err := tbl.RateOn("EUR", "USD", hiddenMarch(5)); !errors.Is(err, fx.ErrNoRate) {
			t.Errorf("err = %v, want ErrNoRate", err)
		}
	})
}
