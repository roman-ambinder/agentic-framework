package report

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/ledger"
	"example.com/ledger/money"
)

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// march returns midnight UTC on the given day of March 2026.
func march(d int) time.Time {
	return time.Date(2026, time.March, d, 0, 0, 0, 0, time.UTC)
}

// sampleStatement returns a small EUR asset statement built by hand.
func sampleStatement(memo string) ledger.Statement {
	return ledger.Statement{
		Account:  account.Account{ID: "assets:bank", Name: "Bank", Kind: account.Asset, Currency: "EUR"},
		From:     march(1),
		To:       march(31),
		Currency: "EUR",
		Opening:  money.New(10000, "EUR"),
		Lines: []ledger.Line{
			{EntryID: 2, Date: march(5), Memo: "Salary", Amount: money.New(5000, "EUR"), Balance: money.New(15000, "EUR")},
			{EntryID: 3, Date: march(10), Memo: memo, Amount: money.New(-3000, "EUR"), Balance: money.New(12000, "EUR")},
		},
		Closing: money.New(12000, "EUR"),
	}
}

func TestWriteStatementLayout(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteStatement(&buf, sampleStatement("Rent")); err != nil {
		t.Fatalf("WriteStatement: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 9 {
		t.Fatalf("statement has %d lines, want 9:\n%s", len(lines), buf.String())
	}
	checks := []struct {
		line int
		want string
	}{
		{0, "Account:  assets:bank Bank"},
		{1, "Period:   2026-03-01 to 2026-03-31"},
		{2, "Currency: EUR"},
		{3, ""},
		{4, "Date"},
		{5, "Opening balance"},
		{6, "2026-03-05  #2"},
		{7, "2026-03-10  #3"},
		{8, "Closing balance"},
	}
	for _, c := range checks {
		if !strings.Contains(lines[c.line], c.want) {
			t.Errorf("line %d = %q, want it to contain %q", c.line, lines[c.line], c.want)
		}
	}
	if !strings.HasSuffix(lines[4], "Balance") {
		t.Errorf("header %q does not end with Balance", lines[4])
	}
	for _, line := range []struct {
		idx    int
		suffix string
	}{
		{5, "100.00 EUR"}, {6, "150.00 EUR"}, {7, "120.00 EUR"}, {8, "120.00 EUR"},
	} {
		if !strings.HasSuffix(lines[line.idx], line.suffix) {
			t.Errorf("line %d = %q, want it to end with %q", line.idx, lines[line.idx], line.suffix)
		}
	}
	if !strings.Contains(lines[6], "Salary") || !strings.Contains(lines[6], "50.00 EUR") {
		t.Errorf("salary line = %q, want memo and amount", lines[6])
	}
}

func TestWriteStatementReportsWriteError(t *testing.T) {
	if err := WriteStatement(failingWriter{}, sampleStatement("Rent")); err == nil {
		t.Error("WriteStatement to a failing writer returned nil error")
	}
}

func TestWriteCSVHeaderAndRows(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, sampleStatement("Rent")); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	want := "date,entry,memo,amount,balance\n" +
		"2026-03-05,2,Salary,50.00 EUR,150.00 EUR\n" +
		"2026-03-10,3,Rent,-30.00 EUR,120.00 EUR\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteCSV =\n%s\nwant\n%s", got, want)
	}
}

func TestWriteCSVEmptyStatementHasOnlyHeader(t *testing.T) {
	var buf bytes.Buffer
	s := sampleStatement("Rent")
	s.Lines = nil
	if err := WriteCSV(&buf, s); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	if got, want := buf.String(), "date,entry,memo,amount,balance\n"; got != want {
		t.Errorf("WriteCSV = %q, want %q", got, want)
	}
}

func TestWriteCSVReportsWriteError(t *testing.T) {
	if err := WriteCSV(failingWriter{}, sampleStatement("Rent")); err == nil {
		t.Error("WriteCSV to a failing writer returned nil error")
	}
}

// readCSV parses CSV the way a spreadsheet would and fails the test on any error.
func readCSV(t *testing.T, data string) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatalf("encoding/csv cannot read output %q: %v", data, err)
	}
	return rows
}

func TestWriteCSVQuotesQuotes(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, sampleStatement(`Said "hi" twice`)); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	if !strings.Contains(buf.String(), `"Said ""hi"" twice"`) {
		t.Errorf("output does not quote and double the quotes:\n%s", buf.String())
	}
	rows := readCSV(t, buf.String())
	if got := rows[2][2]; got != `Said "hi" twice` {
		t.Errorf("memo read back as %q, want %q", got, `Said "hi" twice`)
	}
}

func TestWriteCSVQuotesNewlines(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, sampleStatement("Line one\nLine two")); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	rows := readCSV(t, buf.String())
	if len(rows) != 3 {
		t.Fatalf("read %d rows, want 3", len(rows))
	}
	for i, row := range rows {
		if len(row) != 5 {
			t.Errorf("row %d has %d fields, want 5: %q", i, len(row), row)
		}
	}
	if got := rows[2][2]; got != "Line one\nLine two" {
		t.Errorf("memo read back as %q, want the two-line memo", got)
	}
}

func TestCSVFieldLeavesPlainText(t *testing.T) {
	for _, s := range []string{"", "Rent", "Salary March"} {
		if got := csvField(s); got != s {
			t.Errorf("csvField(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestCSVFieldQuotesSpecialCharacters(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "quote", in: `say "hi"`, want: `"say ""hi"""`},
		{name: "carriage return", in: "a\rb", want: "\"a\rb\""},
		{name: "newline", in: "a\nb", want: "\"a\nb\""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := csvField(tt.in); got != tt.want {
				t.Errorf("csvField(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
