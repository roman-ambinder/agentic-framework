package agentconfig

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var moneyRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(\.([0-9]{1,6}))?$`)

// ParseMicroUSD parses a decimal USD amount ("1", "1.5", "0.000001") into
// micro-USD without floating point. "" -> 0. Max 6 decimals, no sign, no
// exponent, at most 9 digits before the point.
func ParseMicroUSD(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	m := moneyRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("agentconfig: %q is not a decimal amount with at most 6 decimals", s)
	}
	whole, _ := strconv.ParseInt(m[1], 10, 64)
	frac := m[3] + strings.Repeat("0", 6-len(m[3]))
	f, _ := strconv.ParseInt(frac, 10, 64)
	return whole*1_000_000 + f, nil
}
