// Package fx converts amounts between currencies with exchange rates that are
// valid from a date onward.
package fx

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"example.com/ledger/money"
)

// ErrNoRate reports that no rate is in force for the requested pair and date.
var ErrNoRate = errors.New("fx: no rate")

// oneUnit is the rate multiplier for "1 unit to 1 unit": rates are stored in millionths.
const (
	oneUnit    = 1_000_000
	dateLayout = "2006-01-02"
)

// Rate says 1 unit of From = Micros/1_000_000 units of To, valid from Date on.
type Rate struct {
	From, To money.Currency
	Date     time.Time
	Micros   int64 // > 0
}

// pair identifies a direction of conversion.
type pair struct {
	from, to money.Currency
}

// Table holds the rates for every currency pair, sorted by date.
type Table struct {
	rates map[pair][]Rate
}

// NewTable returns an empty rate table.
func NewTable() *Table {
	return &Table{rates: make(map[pair][]Rate)}
}

// Add stores a rate. A rate must be positive. A rate for the same pair and date
// replaces the old one.
func (t *Table) Add(r Rate) error {
	if r.Micros <= 0 {
		return fmt.Errorf("fx: rate %s to %s on %s: micros must be positive, got %d",
			r.From, r.To, r.Date.Format(dateLayout), r.Micros)
	}
	p := pair{from: r.From, to: r.To}
	rs := t.rates[p]
	i := sort.Search(len(rs), func(i int) bool { return !rs[i].Date.Before(r.Date) })
	if i < len(rs) && rs[i].Date.Equal(r.Date) {
		rs[i] = r
	} else {
		rs = slices.Insert(rs, i, r)
	}
	t.rates[p] = rs
	return nil
}

// RateOn returns the rate in force on date: the rate with the latest Date that is
// on or before date. The same currency has rate 1_000_000. If there are no From→To
// rates at all, the inverse of the To→From rates is used:
// round_half_away(1e12 / inverse.Micros). It returns ErrNoRate when no rate is in
// force on date.
func (t *Table) RateOn(from, to money.Currency, date time.Time) (int64, error) {
	if from == to {
		return oneUnit, nil
	}
	if rs := t.rates[pair{from: from, to: to}]; len(rs) > 0 {
		return microsOn(rs, from, to, date)
	}
	micros, err := microsOn(t.rates[pair{from: to, to: from}], from, to, date)
	if err != nil {
		return 0, err
	}
	return roundDiv(oneUnit*oneUnit, micros), nil
}

// Convert converts a into currency to using RateOn(a.Currency, to, date):
// minor = round_half_away(a.Minor * micros / 1e6).
func (t *Table) Convert(a money.Amount, to money.Currency, date time.Time) (money.Amount, error) {
	micros, err := t.RateOn(a.Currency, to, date)
	if err != nil {
		return money.Amount{}, err
	}
	return money.New(roundDiv(a.Minor*micros, oneUnit), to), nil
}

// microsOn returns the micros of the latest rate in rs that is on or before date.
// rs must be sorted by date.
func microsOn(rs []Rate, from, to money.Currency, date time.Time) (int64, error) {
	i := sort.Search(len(rs), func(i int) bool { return rs[i].Date.After(date) })
	if i == 0 {
		return 0, fmt.Errorf("%w: %s to %s on %s", ErrNoRate, from, to, date.Format(dateLayout))
	}
	return rs[i-1].Micros, nil
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
