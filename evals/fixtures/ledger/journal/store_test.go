package journal

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"example.com/ledger/money"
)

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// sameEntries reports whether two entry lists hold the same data.
func sameEntries(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x.ID != y.ID || !x.Date.Equal(y.Date) || x.Memo != y.Memo || x.Reverses != y.Reverses {
			return false
		}
		if len(x.Postings) != len(y.Postings) {
			return false
		}
		for k := range x.Postings {
			if x.Postings[k] != y.Postings[k] {
				return false
			}
		}
	}
	return true
}

func TestSaveLoadRoundTrip(t *testing.T) {
	reg := testRegistry(t)
	j := New(reg)
	mustPost(t, j, march(1), "sale", sale(100000)...)
	mustPost(t, j, march(2), "bank fee", Posting{Account: "expenses:fees", Amount: money.New(1250, "EUR")}, Posting{Account: "assets:bank", Amount: money.New(-1250, "EUR")})
	if _, err := j.Reverse(1, march(3)); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	var buf bytes.Buffer
	if err := j.Save(&buf); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(&buf, reg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !sameEntries(loaded.Entries(), j.Entries()) {
		t.Errorf("loaded entries differ:\n got %+v\nwant %+v", loaded.Entries(), j.Entries())
	}
}

func TestSaveWritesVersionDatesAndAmounts(t *testing.T) {
	j := New(testRegistry(t))
	mustPost(t, j, march(1), "sale", sale(1000)...)
	var buf bytes.Buffer
	if err := j.Save(&buf); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out := buf.String()
	for _, want := range []string{`"version": 1`, `"date": "2026-03-01"`, `"amount": "10.00 EUR"`, `"amount": "-10.00 EUR"`} {
		if !strings.Contains(out, want) {
			t.Errorf("saved JSON lacks %s:\n%s", want, out)
		}
	}
}

func TestSaveEmptyJournal(t *testing.T) {
	var buf bytes.Buffer
	if err := New(testRegistry(t)).Save(&buf); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(&buf, testRegistry(t))
	if err != nil {
		t.Fatalf("Load of empty journal: %v", err)
	}
	if got := loaded.Entries(); len(got) != 0 {
		t.Errorf("loaded %d entries from an empty journal", len(got))
	}
}

func TestSaveReportsWriteError(t *testing.T) {
	j := New(testRegistry(t))
	if err := j.Save(failingWriter{}); err == nil {
		t.Error("Save to a failing writer returned nil error")
	}
}

func TestLoadRejectsBadJSON(t *testing.T) {
	_, err := Load(strings.NewReader("{not json"), testRegistry(t))
	if err == nil {
		t.Error("Load of invalid JSON returned nil error")
	}
}

func TestLoadRejectsBadVersion(t *testing.T) {
	_, err := Load(strings.NewReader(`{"version": 2, "entries": []}`), testRegistry(t))
	if err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Errorf("Load of version 2 error = %v, want an unsupported version error", err)
	}
}

func TestLoadRejectsIDGap(t *testing.T) {
	doc := `{"version": 1, "entries": [{"id": 2, "date": "2026-03-01", "memo": "late",
		"postings": [{"account": "assets:bank", "amount": "10.00 EUR"}, {"account": "income:sales", "amount": "-10.00 EUR"}]}]}`
	_, err := Load(strings.NewReader(doc), testRegistry(t))
	if err == nil || !strings.Contains(err.Error(), "without gaps") {
		t.Errorf("Load with ID 2 first error = %v, want a gap error", err)
	}
}

func TestLoadRejectsBadEntryFields(t *testing.T) {
	balanced := `[{"account": "assets:bank", "amount": "10.00 EUR"}, {"account": "income:sales", "amount": "-10.00 EUR"}]`
	tests := []struct {
		name     string
		postings string
		date     string
	}{
		{name: "bad date", postings: balanced, date: "01/03/2026"},
		{name: "bad amount", postings: `[{"account": "assets:bank", "amount": "10.005 EUR"}, {"account": "income:sales", "amount": "-10.00 EUR"}]`, date: "2026-03-01"},
		{name: "unknown account", postings: `[{"account": "assets:ghost", "amount": "10.00 EUR"}, {"account": "income:sales", "amount": "-10.00 EUR"}]`, date: "2026-03-01"},
		{name: "unbalanced", postings: `[{"account": "assets:bank", "amount": "10.00 EUR"}, {"account": "income:sales", "amount": "-9.00 EUR"}]`, date: "2026-03-01"},
		{name: "one posting", postings: `[{"account": "assets:bank", "amount": "10.00 EUR"}]`, date: "2026-03-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := `{"version": 1, "entries": [{"id": 1, "date": "` + tt.date + `", "memo": "x", "postings": ` + tt.postings + `}]}`
			if _, err := Load(strings.NewReader(doc), testRegistry(t)); err == nil {
				t.Error("Load returned nil error")
			}
		})
	}
}

func TestLoadRejectsReversalOfLaterEntry(t *testing.T) {
	doc := `{"version": 1, "entries": [{"id": 1, "date": "2026-03-01", "memo": "x", "reverses": 1,
		"postings": [{"account": "assets:bank", "amount": "10.00 EUR"}, {"account": "income:sales", "amount": "-10.00 EUR"}]}]}`
	_, err := Load(strings.NewReader(doc), testRegistry(t))
	if err == nil || !strings.Contains(err.Error(), "not an earlier entry") {
		t.Errorf("Load of self-reversal error = %v, want a reversal error", err)
	}
}
