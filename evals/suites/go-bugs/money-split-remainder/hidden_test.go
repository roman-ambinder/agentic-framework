package money_test

import (
	"testing"

	"example.com/ledger/money"
)

func minors(as []money.Amount) []int64 {
	out := make([]int64, len(as))
	for i, a := range as {
		out[i] = a.Minor
	}
	return out
}

func TestHiddenSplitRemainder(t *testing.T) {
	tests := []struct {
		name  string
		minor int64
		n     int
		want  []int64
	}{
		{"ten euros in three", 1000, 3, []int64{334, 333, 333}},
		{"negative ten euros in three", -1000, 3, []int64{-334, -333, -333}},
		{"one cent in four", 1, 4, []int64{1, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := money.New(tt.minor, "EUR").Split(tt.n)
			if err != nil {
				t.Fatalf("Split: %v", err)
			}
			got := minors(parts)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}

	t.Run("parts sum to the amount", func(t *testing.T) {
		for n := 1; n <= 12; n++ {
			for minor := int64(-1001); minor <= 1001; minor += 7 {
				parts, err := money.New(minor, "EUR").Split(n)
				if err != nil {
					t.Fatalf("Split(%d, %d): %v", minor, n, err)
				}
				var sum int64
				lo, hi := parts[0].Minor, parts[0].Minor
				for _, p := range parts {
					sum += p.Minor
					lo, hi = min(lo, p.Minor), max(hi, p.Minor)
				}
				if sum != minor {
					t.Fatalf("Split(%d, %d) parts %v sum to %d", minor, n, minors(parts), sum)
				}
				if hi-lo > 1 {
					t.Fatalf("Split(%d, %d) parts %v differ by more than 1", minor, n, minors(parts))
				}
			}
		}
	})
}
