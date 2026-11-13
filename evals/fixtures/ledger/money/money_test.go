package money

import (
	"errors"
	"testing"
)

func TestParseAcceptsAmounts(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Amount
	}{
		{name: "two decimals", input: "12.34 EUR", want: New(1234, "EUR")},
		{name: "negative", input: "-0.50 USD", want: New(-50, "USD")},
		{name: "whole number", input: "12 EUR", want: New(1200, "EUR")},
		{name: "one decimal", input: "0.5 EUR", want: New(50, "EUR")},
		{name: "zero", input: "0 GBP", want: New(0, "GBP")},
		{name: "negative whole", input: "-3 EUR", want: New(-300, "EUR")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseRejectsThreeDecimals(t *testing.T) {
	_, err := Parse("12.345 EUR")
	if !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("Parse(\"12.345 EUR\") error = %v, want ErrInvalidAmount", err)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "no currency", input: "12.34"},
		{name: "lower-case currency", input: "12.34 eur"},
		{name: "long currency", input: "12.34 EURO"},
		{name: "no space", input: "12.34EUR"},
		{name: "thousands separator", input: "1,234.56 EUR"},
		{name: "leading dot", input: ".5 EUR"},
		{name: "trailing dot", input: "12. EUR"},
		{name: "plus sign", input: "+12 EUR"},
		{name: "letters", input: "abc EUR"},
		{name: "overflow", input: "99999999999999999999 EUR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			if !errors.Is(err, ErrInvalidAmount) {
				t.Errorf("Parse(%q) error = %v, want ErrInvalidAmount", tt.input, err)
			}
		})
	}
}

func TestStringFormatsTwoDecimals(t *testing.T) {
	tests := []struct {
		name string
		in   Amount
		want string
	}{
		{name: "positive", in: New(1234, "EUR"), want: "12.34 EUR"},
		{name: "zero", in: New(0, "EUR"), want: "0.00 EUR"},
		{name: "negative above one", in: New(-105, "EUR"), want: "-1.05 EUR"},
		{name: "large", in: New(100000, "USD"), want: "1000.00 USD"},
		{name: "one cent", in: New(1, "GBP"), want: "0.01 GBP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseStringRoundTrip(t *testing.T) {
	for _, minor := range []int64{0, 1, 99, 100, 12345, 1000000, -100, -999, -123456} {
		a := New(minor, "CHF")
		got, err := Parse(a.String())
		if err != nil {
			t.Fatalf("Parse(%q): %v", a.String(), err)
		}
		if got != a {
			t.Errorf("Parse(String(%d)) = %+v, want %+v", minor, got, a)
		}
	}
}

func TestAddSameCurrency(t *testing.T) {
	got, err := New(1000, "EUR").Add(New(234, "EUR"))
	if err != nil {
		t.Fatalf("Add: unexpected error: %v", err)
	}
	if want := New(1234, "EUR"); got != want {
		t.Errorf("Add = %+v, want %+v", got, want)
	}
}

func TestAddRejectsMismatch(t *testing.T) {
	_, err := New(1000, "EUR").Add(New(100, "USD"))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Add across currencies error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestSubSameCurrency(t *testing.T) {
	got, err := New(1000, "EUR").Sub(New(250, "EUR"))
	if err != nil {
		t.Fatalf("Sub: unexpected error: %v", err)
	}
	if want := New(750, "EUR"); got != want {
		t.Errorf("Sub = %+v, want %+v", got, want)
	}
}

func TestSubRejectsMismatch(t *testing.T) {
	_, err := New(1000, "EUR").Sub(New(100, "USD"))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Sub across currencies error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestNegFlipsSign(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want int64
	}{
		{name: "positive", in: 500, want: -500},
		{name: "negative", in: -500, want: 500},
		{name: "zero", in: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := New(tt.in, "EUR").Neg(); got.Minor != tt.want || got.Currency != "EUR" {
				t.Errorf("Neg(%d) = %+v, want minor %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsZero(t *testing.T) {
	if !New(0, "EUR").IsZero() {
		t.Error("IsZero of zero amount = false, want true")
	}
	if New(1, "EUR").IsZero() {
		t.Error("IsZero of 0.01 EUR = true, want false")
	}
}

func TestMulRateRoundsHalfAwayFromZero(t *testing.T) {
	tests := []struct {
		name  string
		in    Amount
		basis int64
		want  Amount
	}{
		{name: "half rounds up", in: New(25, "EUR"), basis: 1000, want: New(3, "EUR")},
		{name: "negative half rounds away", in: New(-25, "EUR"), basis: 1000, want: New(-3, "EUR")},
		{name: "below half rounds down", in: New(24, "EUR"), basis: 1000, want: New(2, "EUR")},
		{name: "negative below half", in: New(-24, "EUR"), basis: 1000, want: New(-2, "EUR")},
		{name: "fifteen percent", in: New(1000, "USD"), basis: 1500, want: New(150, "USD")},
		{name: "identity", in: New(1234, "USD"), basis: 10000, want: New(1234, "USD")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.MulRate(tt.basis); got != tt.want {
				t.Errorf("MulRate(%d) = %+v, want %+v", tt.basis, got, tt.want)
			}
		})
	}
}

func TestSplitEvenly(t *testing.T) {
	parts, err := New(900, "EUR").Split(3)
	if err != nil {
		t.Fatalf("Split: unexpected error: %v", err)
	}
	want := []Amount{New(300, "EUR"), New(300, "EUR"), New(300, "EUR")}
	if len(parts) != len(want) {
		t.Fatalf("Split returned %d parts, want %d", len(parts), len(want))
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Errorf("part %d = %+v, want %+v", i, parts[i], want[i])
		}
	}
}

func TestSplitRejectsNonPositiveParts(t *testing.T) {
	for _, n := range []int{0, -1} {
		_, err := New(900, "EUR").Split(n)
		if !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("Split(%d) error = %v, want ErrInvalidAmount", n, err)
		}
	}
}

func TestSumEmptyIsZero(t *testing.T) {
	got, err := Sum("USD")
	if err != nil {
		t.Fatalf("Sum: unexpected error: %v", err)
	}
	if want := New(0, "USD"); got != want {
		t.Errorf("Sum() = %+v, want %+v", got, want)
	}
}

func TestSumAddsAmounts(t *testing.T) {
	got, err := Sum("EUR", New(100, "EUR"), New(250, "EUR"), New(-50, "EUR"))
	if err != nil {
		t.Fatalf("Sum: unexpected error: %v", err)
	}
	if want := New(300, "EUR"); got != want {
		t.Errorf("Sum = %+v, want %+v", got, want)
	}
}

func TestSumRejectsMismatch(t *testing.T) {
	_, err := Sum("EUR", New(100, "EUR"), New(100, "USD"))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Sum across currencies error = %v, want ErrCurrencyMismatch", err)
	}
}
