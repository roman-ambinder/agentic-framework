package money_test

import (
	"testing"

	"example.com/ledger/money"
)

func TestHiddenStringNegativeCents(t *testing.T) {
	t.Run("small negatives", func(t *testing.T) {
		tests := []struct {
			minor int64
			want  string
		}{
			{-5, "-0.05 EUR"},
			{-99, "-0.99 EUR"},
			{-105, "-1.05 EUR"},
		}
		for _, tt := range tests {
			if got := money.New(tt.minor, "EUR").String(); got != tt.want {
				t.Errorf("New(%d).String() = %q, want %q", tt.minor, got, tt.want)
			}
		}
	})

	t.Run("round trip", func(t *testing.T) {
		for minor := int64(-250); minor <= 250; minor++ {
			a := money.New(minor, "EUR")
			got, err := money.Parse(a.String())
			if err != nil {
				t.Fatalf("Parse(%q): %v", a.String(), err)
			}
			if got != a {
				t.Fatalf("Parse(%q) = %v, want %v", a.String(), got, a)
			}
		}
	})
}
