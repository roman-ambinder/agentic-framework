package agentconfig

import (
	"strings"
	"testing"
)

// moneyAgent is a minimal agent file with one budget value and one price.
func moneyAgent(maxUSD, inputPrice string) []byte {
	return []byte(`name: a
version: 1
model:
  provider: anthropic
  name: m
  pricing:
    input_per_mtok: ` + inputPrice + `
budget:
  max_usd: ` + maxUSD + `
policy: {default: deny}
`)
}

// TestMoneyKeepsSourceText checks that money values are read exactly as
// written: YAML would otherwise turn an unquoted 010 into 8 (octal) or
// 0.000001 into 1e-06 before it reaches ParseMicroUSD.
func TestMoneyKeepsSourceText(t *testing.T) {
	tests := []struct {
		name          string
		maxUSD, price string
		wantUSD       int64
		wantPrice     int64
		wantErr       string
	}{
		{name: "plain decimals", maxUSD: "2.50", price: "3.00", wantUSD: 2_500_000, wantPrice: 3_000_000},
		{name: "quoted", maxUSD: `"2.50"`, price: `"3"`, wantUSD: 2_500_000, wantPrice: 3_000_000},
		{name: "smallest unit", maxUSD: "0.000001", price: "0.000001", wantUSD: 1, wantPrice: 1},
		{name: "leading zero is rejected, not read as octal", maxUSD: "010", price: "1", wantErr: "budget.max_usd"},
		{name: "octal price is rejected", maxUSD: "1", price: "010", wantErr: "input_per_mtok"},
		{name: "hex is rejected", maxUSD: "0x10", price: "1", wantErr: "budget.max_usd"},
		{name: "exponent is rejected", maxUSD: "1e2", price: "1", wantErr: "budget.max_usd"},
		{name: "underscores are rejected", maxUSD: "1_000", price: "1", wantErr: "budget.max_usd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := Parse(moneyAgent(tt.maxUSD, tt.price))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Parse error = %v, want one naming %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := a.BudgetProto().GetMaxUsdMicros(); got != tt.wantUSD {
				t.Errorf("max_usd = %d micros, want %d", got, tt.wantUSD)
			}
			if got := a.Price().InputPerMTok; got != tt.wantPrice {
				t.Errorf("input_per_mtok = %d micros, want %d", got, tt.wantPrice)
			}
		})
	}
}

// TestMoneyRejectsNonScalars checks that a list or map in a money field is an
// error, as it was before.
func TestMoneyRejectsNonScalars(t *testing.T) {
	if _, err := Parse(moneyAgent("[1]", "1")); err == nil {
		t.Fatal("Parse accepted max_usd: [1]")
	}
}

// TestMoneyStructsStayStrict checks that budget and pricing still reject
// unknown keys now that they decode through their own UnmarshalYAML.
func TestMoneyStructsStayStrict(t *testing.T) {
	tests := []struct{ name, maxUSD, price, want string }{
		{name: "budget", maxUSD: "1\n  max_dollars: 2", price: "1", want: "max_dollars"},
		{name: "pricing", maxUSD: "1", price: "1\n    input_per_token: 2", want: "input_per_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(moneyAgent(tt.maxUSD, tt.price))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse error = %v, want one naming %s", err, tt.want)
			}
		})
	}
}
