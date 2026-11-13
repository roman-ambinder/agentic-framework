package agentconfig

import "testing"

func TestParseMicroUSD(t *testing.T) {
	ok := map[string]int64{"": 0, "0": 0, "1": 1000000, "1.5": 1500000, "0.000001": 1, "12.345678": 12345678, "1.875": 1875000}
	for in, want := range ok {
		t.Run("ok "+in, func(t *testing.T) {
			got, err := ParseMicroUSD(in)
			if err != nil || got != want {
				t.Fatalf("ParseMicroUSD(%q) = %d, %v; want %d", in, got, err, want)
			}
		})
	}
	for _, in := range []string{"-1", "1e3", "1.2345678", "01", "abc", " 1", "1.", "1234567890"} {
		t.Run("bad "+in, func(t *testing.T) {
			if _, err := ParseMicroUSD(in); err == nil {
				t.Fatalf("ParseMicroUSD(%q): want error", in)
			}
		})
	}
}
