package journal

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"example.com/ledger/account"
	"example.com/ledger/money"
)

const (
	fileVersion = 1
	dateLayout  = "2006-01-02"
)

// file is the JSON document written by Save.
type file struct {
	Version int         `json:"version"`
	Entries []fileEntry `json:"entries"`
}

type fileEntry struct {
	ID       int           `json:"id"`
	Date     string        `json:"date"`
	Memo     string        `json:"memo"`
	Reverses int           `json:"reverses,omitempty"`
	Postings []filePosting `json:"postings"`
}

type filePosting struct {
	Account string `json:"account"`
	Amount  string `json:"amount"`
}

// Save writes the journal as JSON: {"version":1,"entries":[...]} with dates as "2006-01-02"
// and amounts as strings in money.String format.
func (j *Journal) Save(w io.Writer) error {
	f := file{Version: fileVersion, Entries: make([]fileEntry, 0, len(j.entries))}
	for _, e := range j.entries {
		fe := fileEntry{
			ID:       e.ID,
			Date:     e.Date.Format(dateLayout),
			Memo:     e.Memo,
			Reverses: e.Reverses,
			Postings: make([]filePosting, 0, len(e.Postings)),
		}
		for _, p := range e.Postings {
			fe.Postings = append(fe.Postings, filePosting{Account: p.Account, Amount: p.Amount.String()})
		}
		f.Entries = append(f.Entries, fe)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return fmt.Errorf("journal: save: %w", err)
	}
	return nil
}

// Load reads what Save wrote, re-validates every entry against reg, and rebuilds all internal
// state, including which entries have been reversed (from each entry's Reverses field).
func Load(r io.Reader, reg *account.Registry) (*Journal, error) {
	var f file
	if err := json.NewDecoder(r).Decode(&f); err != nil {
		return nil, fmt.Errorf("journal: load: %w", err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("journal: load: unsupported version %d", f.Version)
	}
	j := New(reg)
	for _, fe := range f.Entries {
		if err := j.loadEntry(fe); err != nil {
			return nil, err
		}
	}
	return j, nil
}

// loadEntry parses one stored entry and appends it after the entries already loaded.
func (j *Journal) loadEntry(fe fileEntry) error {
	if fe.ID != len(j.entries)+1 {
		return fmt.Errorf("journal: load: entry %d: IDs must run 1, 2, 3 without gaps", fe.ID)
	}
	date, err := time.Parse(dateLayout, fe.Date)
	if err != nil {
		return fmt.Errorf("journal: load: entry %d: date: %w", fe.ID, err)
	}
	postings := make([]Posting, 0, len(fe.Postings))
	for _, p := range fe.Postings {
		amt, err := money.Parse(p.Amount)
		if err != nil {
			return fmt.Errorf("journal: load: entry %d: %w", fe.ID, err)
		}
		postings = append(postings, Posting{Account: p.Account, Amount: amt})
	}
	if err := j.validate(postings); err != nil {
		return fmt.Errorf("journal: load: entry %d: %w", fe.ID, err)
	}
	if fe.Reverses != 0 {
		if err := j.checkReversal(fe); err != nil {
			return err
		}
	}
	j.append(date, fe.Memo, postings, fe.Reverses)
	return nil
}

// checkReversal checks that a stored reversal points at an earlier entry that is not reversed yet.
func (j *Journal) checkReversal(fe fileEntry) error {
	if fe.Reverses < 1 || fe.Reverses >= fe.ID {
		return fmt.Errorf("journal: load: entry %d: reverses #%d, which is not an earlier entry", fe.ID, fe.Reverses)
	}
	if by, done := j.reversed[fe.Reverses]; done {
		return fmt.Errorf("journal: load: entry %d: #%d was already reversed by #%d", fe.ID, fe.Reverses, by)
	}
	return nil
}
