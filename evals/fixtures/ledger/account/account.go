// Package account defines the chart of accounts: the kind, currency and open or
// closed state of every account that postings can refer to.
package account

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	"example.com/ledger/money"
)

// Kind classifies an account for the balance sheet and the income statement.
type Kind int

// The account kinds.
const (
	Asset Kind = iota + 1
	Liability
	Equity
	Income
	Expense
)

// String returns the lower-case name of the kind.
func (k Kind) String() string {
	switch k {
	case Asset:
		return "asset"
	case Liability:
		return "liability"
	case Equity:
		return "equity"
	case Income:
		return "income"
	case Expense:
		return "expense"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Normal is +1 for debit-normal kinds (Asset, Expense) and -1 for credit-normal
// kinds (Liability, Equity, Income). It is 0 for an invalid kind.
func (k Kind) Normal() int64 {
	switch k {
	case Asset, Expense:
		return 1
	case Liability, Equity, Income:
		return -1
	}
	return 0
}

// valid reports whether k is one of the defined kinds.
func (k Kind) valid() bool {
	return k >= Asset && k <= Expense
}

// Account is one entry in the chart of accounts.
type Account struct {
	ID       string // ^[a-z0-9][a-z0-9:-]{0,63}$, e.g. "assets:bank", "liabilities:customer:42"
	Name     string
	Kind     Kind
	Currency money.Currency
	Closed   bool
}

var (
	// ErrInvalidAccount reports an account with a bad ID, kind or currency.
	ErrInvalidAccount = errors.New("account: invalid account")
	// ErrDuplicate reports an attempt to open an ID that is already in use.
	ErrDuplicate = errors.New("account: duplicate id")
	// ErrNotFound reports an ID that is not in the registry.
	ErrNotFound = errors.New("account: not found")
)

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9:-]{0,63}$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

// Registry holds the accounts of one ledger, keyed by ID. Use NewRegistry to create one.
type Registry struct {
	accounts map[string]Account
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{accounts: make(map[string]Account)}
}

// Open adds a new account. It checks the ID, the kind and the currency, and
// rejects an ID that is already in use. The account is always opened (Closed is false).
func (r *Registry) Open(a Account) error {
	if !idPattern.MatchString(a.ID) || !a.Kind.valid() || !currencyPattern.MatchString(string(a.Currency)) {
		return fmt.Errorf("%w: id %q, kind %v, currency %q", ErrInvalidAccount, a.ID, a.Kind, a.Currency)
	}
	if _, exists := r.accounts[a.ID]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicate, a.ID)
	}
	a.Closed = false
	r.accounts[a.ID] = a
	return nil
}

// Get returns the account with the given ID, if there is one.
func (r *Registry) Get(id string) (Account, bool) {
	a, ok := r.accounts[id]
	return a, ok
}

// Close marks an account as closed. Postings to a closed account are refused.
func (r *Registry) Close(id string) error {
	a, ok := r.accounts[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	a.Closed = true
	r.accounts[id] = a
	return nil
}

// List returns all accounts sorted by ID.
func (r *Registry) List() []Account {
	ids := slices.Sorted(maps.Keys(r.accounts))
	out := make([]Account, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.accounts[id])
	}
	return out
}
