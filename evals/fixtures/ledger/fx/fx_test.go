package fx

import (
	"errors"
	"testing"
	"time"

	"example.com/ledger/money"
)

func day(m time.Month, d int) time.Time {
	return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC)
}

// eurUSD returns a table with EUR→USD rates 1.10 on 2026-03-10 and 1.20 on 2026-03-20.
func eurUSD(t *testing.T) *Table {
	t.Helper()
	tbl := NewTable()
	rates := []Rate{
		{From: "EUR", To: "USD", Date: day(3, 20), Micros: 1_200_000},
		{From: "EUR", To: "USD", Date: day(3, 10), Micros: 1_100_000},
	}
	for _, r := range rates {
		if err := tbl.Add(r); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	return tbl
}

func TestAddRejectsNonPositiveMicros(t *testing.T) {
	for _, micros := range []int64{0, -1} {
		err := NewTable().Add(Rate{From: "EUR", To: "USD", Date: day(3, 1), Micros: micros})
		if err == nil {
			t.Errorf("Add with micros %d returned nil error", micros)
		}
	}
}

func TestAddReplacesSameDate(t *testing.T) {
	tbl := eurUSD(t)
	if err := tbl.Add(Rate{From: "EUR", To: "USD", Date: day(3, 10), Micros: 1_120_000}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := tbl.RateOn("EUR", "USD", day(3, 10))
	if err != nil || got != 1_120_000 {
		t.Errorf("RateOn after replacement = %d, %v; want 1120000, nil", got, err)
	}
}

func TestRateOnExactDate(t *testing.T) {
	tbl := eurUSD(t)
	tests := []struct {
		date time.Time
		want int64
	}{
		{day(3, 10), 1_100_000},
		{day(3, 20), 1_200_000},
	}
	for _, tt := range tests {
		got, err := tbl.RateOn("EUR", "USD", tt.date)
		if err != nil || got != tt.want {
			t.Errorf("RateOn(%s) = %d, %v; want %d, nil", tt.date.Format("2006-01-02"), got, err, tt.want)
		}
	}
}

func TestRateOnSameCurrency(t *testing.T) {
	got, err := NewTable().RateOn("EUR", "EUR", day(3, 1))
	if err != nil || got != 1_000_000 {
		t.Errorf("RateOn(EUR, EUR) = %d, %v; want 1000000, nil", got, err)
	}
}

func TestRateOnUnknownPairIsNoRate(t *testing.T) {
	_, err := eurUSD(t).RateOn("EUR", "GBP", day(3, 20))
	if !errors.Is(err, ErrNoRate) {
		t.Errorf("RateOn for unknown pair error = %v, want ErrNoRate", err)
	}
}

func TestRateOnInverseOfExactRate(t *testing.T) {
	tbl := NewTable()
	if err := tbl.Add(Rate{From: "EUR", To: "USD", Date: day(1, 1), Micros: 1_100_000}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// 1e12 / 1_100_000 = 909090.9..., which rounds to 909091.
	got, err := tbl.RateOn("USD", "EUR", day(1, 1))
	if err != nil || got != 909_091 {
		t.Errorf("inverse RateOn = %d, %v; want 909091, nil", got, err)
	}
}

func TestConvertExactDate(t *testing.T) {
	got, err := eurUSD(t).Convert(money.New(10000, "EUR"), "USD", day(3, 10))
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if want := money.New(11000, "USD"); got != want {
		t.Errorf("Convert 100.00 EUR = %+v, want %+v", got, want)
	}
}

func TestConvertRoundsHalfAwayFromZero(t *testing.T) {
	tbl := NewTable()
	if err := tbl.Add(Rate{From: "EUR", To: "USD", Date: day(1, 1), Micros: 1_500_000}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	tests := []struct {
		name string
		in   int64
		want int64
	}{
		{name: "positive half", in: 1, want: 2},
		{name: "negative half", in: -1, want: -2},
		{name: "zero", in: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tbl.Convert(money.New(tt.in, "EUR"), "USD", day(1, 1))
			if err != nil || got.Minor != tt.want || got.Currency != "USD" {
				t.Errorf("Convert(%d) = %+v, %v; want minor %d USD", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestConvertSameCurrencyKeepsAmount(t *testing.T) {
	in := money.New(4321, "GBP")
	got, err := NewTable().Convert(in, "GBP", day(1, 1))
	if err != nil || got != in {
		t.Errorf("Convert to same currency = %+v, %v; want %+v, nil", got, err, in)
	}
}

func TestConvertWithoutRateFails(t *testing.T) {
	_, err := NewTable().Convert(money.New(100, "EUR"), "USD", day(1, 1))
	if !errors.Is(err, ErrNoRate) {
		t.Errorf("Convert without rates error = %v, want ErrNoRate", err)
	}
}
