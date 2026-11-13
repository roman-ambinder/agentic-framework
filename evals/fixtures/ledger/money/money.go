// Package money holds currency amounts as integer minor units (cents) and the
// arithmetic the ledger needs. It never uses floating-point numbers.
package money

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 style code: three upper-case ASCII letters, such as "EUR".
type Currency string

var (
	// ErrInvalidAmount reports a malformed amount or an invalid split count.
	ErrInvalidAmount = errors.New("money: invalid amount")
	// ErrCurrencyMismatch reports arithmetic between amounts in different currencies.
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
)

// Amount is a quantity of one currency in minor units. Every currency has two
// decimal places, so 1234 minor units of EUR is 12.34 EUR.
type Amount struct {
	Minor    int64
	Currency Currency
}

// amountPattern matches "<number> <CUR>", where the number has at most two decimals.
var amountPattern = regexp.MustCompile(`^(-?)([0-9]+)(?:\.([0-9]{1,2}))? ([A-Z]{3})$`)

// New returns an amount of the given minor units in currency c.
func New(minor int64, c Currency) Amount {
	return Amount{Minor: minor, Currency: c}
}

// Parse reads an amount written as "<number> <CUR>", for example "12.34 EUR",
// "-0.50 USD", "12 EUR" or "0.5 EUR". Anything else is an error that wraps ErrInvalidAmount.
func Parse(s string) (Amount, error) {
	m := amountPattern.FindStringSubmatch(s)
	if m == nil {
		return Amount{}, fmt.Errorf("%w: %q", ErrInvalidAmount, s)
	}
	sign, whole, frac, cur := m[1], m[2], m[3], m[4]
	// The whole part followed by the fraction padded to two digits is the minor value.
	digits := whole + frac + strings.Repeat("0", 2-len(frac))
	minor, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Amount{}, fmt.Errorf("%w: %q: %w", ErrInvalidAmount, s, err)
	}
	if sign == "-" {
		minor = -minor
	}
	return Amount{Minor: minor, Currency: Currency(cur)}, nil
}

// String formats the amount with exactly two decimals and the currency code, for
// example "12.34 EUR", "-0.05 EUR" or "0.00 EUR". Parse reads the result back.
func (a Amount) String() string {
	sign := ""
	abs := uint64(a.Minor)
	if a.Minor < 0 {
		sign = "-"
		abs = uint64(-(a.Minor + 1)) + 1 // also correct for math.MinInt64
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, abs/100, abs%100, a.Currency)
}

// Add returns a + b. Both amounts must have the same currency.
func (a Amount) Add(b Amount) (Amount, error) {
	if a.Currency != b.Currency {
		return Amount{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, a.Currency, b.Currency)
	}
	return Amount{Minor: a.Minor + b.Minor, Currency: a.Currency}, nil
}

// Sub returns a - b. Both amounts must have the same currency.
func (a Amount) Sub(b Amount) (Amount, error) {
	return a.Add(b.Neg())
}

// Neg returns the amount with the opposite sign.
func (a Amount) Neg() Amount {
	return Amount{Minor: -a.Minor, Currency: a.Currency}
}

// IsZero reports whether the amount is zero.
func (a Amount) IsZero() bool {
	return a.Minor == 0
}

// MulRate multiplies the amount by basisPoints/10000, rounding half away from
// zero: 2.5 becomes 3 and -2.5 becomes -3.
func (a Amount) MulRate(basisPoints int64) Amount {
	return Amount{Minor: roundDiv(a.Minor*basisPoints, 10000), Currency: a.Currency}
}

// Split divides a into n parts (n >= 1) that differ by at most one minor unit and
// sum exactly to a. The larger parts come first: 10.00 / 3 gives 3.34, 3.33, 3.33,
// and -10.00 / 3 gives -3.34, -3.33, -3.33. For n < 1 it returns ErrInvalidAmount.
func (a Amount) Split(n int) ([]Amount, error) {
	if n < 1 {
		return nil, fmt.Errorf("%w: cannot split into %d parts", ErrInvalidAmount, n)
	}
	sign, abs := int64(1), a.Minor
	if abs < 0 {
		sign, abs = -1, -abs
	}
	parts := make([]Amount, n)
	base, extra := abs/int64(n), abs%int64(n)
	for i := range parts {
		m := base
		if int64(i) < extra {
			m++
		}
		parts[i] = Amount{Minor: sign * m, Currency: a.Currency}
	}
	return parts, nil
}

// Sum adds amounts that all have currency c. Empty input gives zero of c.
func Sum(c Currency, as ...Amount) (Amount, error) {
	total := New(0, c)
	for _, a := range as {
		var err error
		if total, err = total.Add(a); err != nil {
			return Amount{}, err
		}
	}
	return total, nil
}

// roundDiv returns n/d rounded half away from zero. d must be positive.
func roundDiv(n, d int64) int64 {
	q, r := n/d, n%d
	if r < 0 {
		r = -r
	}
	if 2*r < d {
		return q
	}
	if n < 0 {
		return q - 1
	}
	return q + 1
}
