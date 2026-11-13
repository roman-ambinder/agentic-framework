package model

import "testing"

func TestPriceCost(t *testing.T) {
	tests := []struct {
		name  string
		price Price
		usage Usage
		want  int64
	}{
		{"zero price", Price{}, Usage{InputTokens: 1000, OutputTokens: 1000}, 0},
		{"one million input", Price{InputPerMTok: 3_000_000}, Usage{InputTokens: 1_000_000}, 3_000_000},
		{"rounds up", Price{InputPerMTok: 1}, Usage{InputTokens: 1}, 1},
		{
			"mixed",
			Price{InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, CacheReadPerMTok: 300_000, CacheWritePerMTok: 3_750_000},
			Usage{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 2000, CacheWriteTokens: 100},
			11_475,
		},
		{"sum rounded once", Price{InputPerMTok: 1, OutputPerMTok: 1}, Usage{InputTokens: 1, OutputTokens: 1}, 1},
		{"negative is zero", Price{InputPerMTok: 1_000_000, OutputPerMTok: 1_000_000}, Usage{InputTokens: -5, OutputTokens: 2}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.price.Cost(tt.usage); got != tt.want {
				t.Fatalf("Cost = %d, want %d", got, tt.want)
			}
		})
	}
}
