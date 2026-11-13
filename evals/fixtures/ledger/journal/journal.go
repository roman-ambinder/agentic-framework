// Package journal records balanced double-entry transactions against the
// accounts of an account registry. Entries are append-only: a mistake is
// corrected by reversing the entry and posting a new one.
package journal

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/money"
)

// Posting moves money into an account (a debit, positive amount) or out of it (a credit, negative amount).
type Posting struct {
	Account string
	Amount  money.Amount // > 0 debit, < 0 credit
}

// Entry is a set of postings made on one date, with an ID that never changes.
type Entry struct {
	ID       int
	Date     time.Time
	Memo     string
	Postings []Posting
	Reverses int // ID of the entry this one reverses; 0 if none
}

var (
	// ErrUnbalanced reports postings whose amounts do not sum to zero in some currency.
	ErrUnbalanced = errors.New("journal: entry does not balance")
	// ErrTooFewPostings reports an entry with fewer than two postings.
	ErrTooFewPostings = errors.New("journal: entry needs at least two postings")
	// ErrUnknownAccount reports a posting to an account that is not in the registry.
	ErrUnknownAccount = errors.New("journal: unknown account")
	// ErrClosedAccount reports a posting to an account that is closed.
	ErrClosedAccount = errors.New("journal: account is closed")
	// ErrWrongCurrency reports a posting in a currency other than the account's.
	ErrWrongCurrency = errors.New("journal: posting currency differs from account currency")
	// ErrNotFound reports an entry ID that does not exist.
	ErrNotFound = errors.New("journal: entry not found")
	// ErrAlreadyReversed reports an attempt to reverse an entry a second time.
	ErrAlreadyReversed = errors.New("journal: entry already reversed")
)

// Journal is an append-only list of entries posted against one registry.
type Journal struct {
	reg      *account.Registry
	entries  []Entry     // in ID order; IDs start at 1, no gaps
	reversed map[int]int // entry ID → ID of the entry that reversed it
}

// New returns an empty journal that checks postings against reg.
func New(reg *account.Registry) *Journal {
	return &Journal{reg: reg, reversed: make(map[int]int)}
}

// Post validates and appends a new entry with the next ID. Rules, checked in this order:
// at least 2 postings; every account exists, is open, and has the posting's currency;
// for each currency the posting amounts sum to zero.
func (j *Journal) Post(date time.Time, memo string, postings ...Posting) (Entry, error) {
	if err := j.validate(postings); err != nil {
		return Entry{}, err
	}
	return j.append(date, memo, postings, 0), nil
}

// Get returns the entry with the given ID, if there is one.
func (j *Journal) Get(id int) (Entry, bool) {
	if id < 1 || id > len(j.entries) {
		return Entry{}, false
	}
	return cloneEntry(j.entries[id-1]), true
}

// Entries returns a copy of all entries in ID order.
func (j *Journal) Entries() []Entry {
	out := make([]Entry, len(j.entries))
	for i, e := range j.entries {
		out[i] = cloneEntry(e)
	}
	return out
}

// Reverse posts a new entry on date with every posting negated, memo
// "Reversal of #<id>: <memo>", and Reverses set to id. It returns ErrNotFound if id
// does not exist and ErrAlreadyReversed if the entry was reversed before. A
// reversal entry can itself be reversed once.
func (j *Journal) Reverse(id int, date time.Time) (Entry, error) {
	orig, ok := j.Get(id)
	if !ok {
		return Entry{}, fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if by, done := j.reversed[id]; done {
		return Entry{}, fmt.Errorf("%w: #%d by #%d", ErrAlreadyReversed, id, by)
	}
	negated := make([]Posting, len(orig.Postings))
	for i, p := range orig.Postings {
		negated[i] = Posting{Account: p.Account, Amount: p.Amount.Neg()}
	}
	if err := j.validate(negated); err != nil {
		return Entry{}, err
	}
	memo := fmt.Sprintf("Reversal of #%d: %s", id, orig.Memo)
	return j.append(date, memo, negated, id), nil
}

// validate applies the posting rules from Post.
func (j *Journal) validate(postings []Posting) error {
	if len(postings) < 2 {
		return ErrTooFewPostings
	}
	for _, p := range postings {
		acc, ok := j.reg.Get(p.Account)
		switch {
		case !ok:
			return fmt.Errorf("%w: %q", ErrUnknownAccount, p.Account)
		case acc.Closed:
			return fmt.Errorf("%w: %q", ErrClosedAccount, p.Account)
		case p.Amount.Currency != acc.Currency:
			return fmt.Errorf("%w: %q is %s, posting is %s", ErrWrongCurrency, p.Account, acc.Currency, p.Amount.Currency)
		}
	}
	return checkBalanced(postings)
}

// checkBalanced reports ErrUnbalanced if the postings do not sum to zero in some currency.
// It walks the postings in order so that the reported currency is always the same.
func checkBalanced(postings []Posting) error {
	sums := make(map[money.Currency]int64)
	for _, p := range postings {
		sums[p.Amount.Currency] += p.Amount.Minor
	}
	for _, p := range postings {
		if sum := sums[p.Amount.Currency]; sum != 0 {
			return fmt.Errorf("%w: %s is off by %d minor units", ErrUnbalanced, p.Amount.Currency, sum)
		}
	}
	return nil
}

// append stores a validated entry with the next ID and records any reversal.
func (j *Journal) append(date time.Time, memo string, postings []Posting, reverses int) Entry {
	e := Entry{
		ID:       len(j.entries) + 1,
		Date:     date,
		Memo:     memo,
		Postings: slices.Clone(postings),
		Reverses: reverses,
	}
	j.entries = append(j.entries, e)
	if reverses != 0 {
		j.reversed[reverses] = e.ID
	}
	return cloneEntry(e)
}

// cloneEntry returns a copy of e that does not share its postings slice.
func cloneEntry(e Entry) Entry {
	e.Postings = slices.Clone(e.Postings)
	return e
}
