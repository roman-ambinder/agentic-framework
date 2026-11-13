package account

import (
	"errors"
	"strings"
	"testing"
)

func TestKindString(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{Asset, "asset"},
		{Liability, "liability"},
		{Equity, "equity"},
		{Income, "income"},
		{Expense, "expense"},
		{Kind(42), "kind(42)"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(tt.kind), got, tt.want)
		}
	}
}

func TestKindNormal(t *testing.T) {
	tests := []struct {
		kind Kind
		want int64
	}{
		{Asset, 1},
		{Expense, 1},
		{Liability, -1},
		{Equity, -1},
		{Income, -1},
		{Kind(0), 0},
	}
	for _, tt := range tests {
		if got := tt.kind.Normal(); got != tt.want {
			t.Errorf("%v.Normal() = %d, want %d", tt.kind, got, tt.want)
		}
	}
}

func TestOpenRejectsBadID(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "empty", id: ""},
		{name: "upper case", id: "Assets:Bank"},
		{name: "leading dash", id: "-bank"},
		{name: "space", id: "assets bank"},
		{name: "too long", id: "a" + strings.Repeat("b", 64)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := NewRegistry()
			err := reg.Open(Account{ID: tt.id, Kind: Asset, Currency: "EUR"})
			if !errors.Is(err, ErrInvalidAccount) {
				t.Errorf("Open(%q) error = %v, want ErrInvalidAccount", tt.id, err)
			}
		})
	}
}

func TestOpenAcceptsLongestValidID(t *testing.T) {
	reg := NewRegistry()
	id := "a" + strings.Repeat("b", 63)
	if err := reg.Open(Account{ID: id, Kind: Asset, Currency: "EUR"}); err != nil {
		t.Errorf("Open(%d-char id) error = %v, want nil", len(id), err)
	}
}

func TestOpenRejectsBadKindOrCurrency(t *testing.T) {
	tests := []struct {
		name string
		acc  Account
	}{
		{name: "zero kind", acc: Account{ID: "assets:a", Kind: 0, Currency: "EUR"}},
		{name: "unknown kind", acc: Account{ID: "assets:a", Kind: 9, Currency: "EUR"}},
		{name: "lower-case currency", acc: Account{ID: "assets:a", Kind: Asset, Currency: "eur"}},
		{name: "long currency", acc: Account{ID: "assets:a", Kind: Asset, Currency: "EURO"}},
		{name: "empty currency", acc: Account{ID: "assets:a", Kind: Asset}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewRegistry().Open(tt.acc)
			if !errors.Is(err, ErrInvalidAccount) {
				t.Errorf("Open(%+v) error = %v, want ErrInvalidAccount", tt.acc, err)
			}
		})
	}
}

func TestOpenRejectsDuplicate(t *testing.T) {
	reg := NewRegistry()
	acc := Account{ID: "assets:bank", Name: "Bank", Kind: Asset, Currency: "EUR"}
	if err := reg.Open(acc); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := reg.Open(acc); !errors.Is(err, ErrDuplicate) {
		t.Errorf("second Open error = %v, want ErrDuplicate", err)
	}
}

func TestOpenClearsClosedFlag(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Open(Account{ID: "assets:bank", Kind: Asset, Currency: "EUR", Closed: true}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, ok := reg.Get("assets:bank")
	if !ok {
		t.Fatal("Get after Open: not found")
	}
	if got.Closed {
		t.Error("Open kept Closed = true, want an open account")
	}
}

func TestGetReturnsOpenedAccount(t *testing.T) {
	reg := NewRegistry()
	want := Account{ID: "income:sales", Name: "Sales", Kind: Income, Currency: "USD"}
	if err := reg.Open(want); err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, ok := reg.Get("income:sales")
	if !ok || got != want {
		t.Errorf("Get = %+v, %v; want %+v, true", got, ok, want)
	}
	if _, ok := reg.Get("income:missing"); ok {
		t.Error("Get of unknown ID returned ok = true")
	}
}

func TestCloseMissingIsNotFound(t *testing.T) {
	err := NewRegistry().Close("assets:nothing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Close of unknown ID error = %v, want ErrNotFound", err)
	}
}

func TestCloseMarksAccountClosed(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Open(Account{ID: "expenses:fees", Kind: Expense, Currency: "EUR"}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := reg.Close("expenses:fees"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, _ := reg.Get("expenses:fees")
	if !got.Closed {
		t.Error("account is not closed after Close")
	}
}

func TestListSortedByID(t *testing.T) {
	reg := NewRegistry()
	for _, id := range []string{"liabilities:customer:2", "assets:bank", "equity:capital", "assets:bank:usd"} {
		if err := reg.Open(Account{ID: id, Kind: Asset, Currency: "EUR"}); err != nil {
			t.Fatalf("Open(%q): %v", id, err)
		}
	}
	list := reg.List()
	want := []string{"assets:bank", "assets:bank:usd", "equity:capital", "liabilities:customer:2"}
	if len(list) != len(want) {
		t.Fatalf("List returned %d accounts, want %d", len(list), len(want))
	}
	for i := range want {
		if list[i].ID != want[i] {
			t.Errorf("List[%d] = %q, want %q", i, list[i].ID, want[i])
		}
	}
}

func TestListEmptyRegistry(t *testing.T) {
	if got := NewRegistry().List(); len(got) != 0 {
		t.Errorf("List of empty registry = %v, want none", got)
	}
}
