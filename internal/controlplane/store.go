// Package controlplane is the control database (.adlc/control.db): kill
// switches (task C.3) and the agent registry (task C.4). It never holds run events.
package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
)

// ErrSchemaTooNew means the database was written by a newer version.
var ErrSchemaTooNew = errors.New("controlplane: database schema is newer than this binary")

// migrations[i] moves the schema from version i to i+1. Never edit an
// existing entry; append a new one (task C.4 appends the registry).
var migrations = []string{
	`CREATE TABLE kills (
		scope             TEXT    NOT NULL CHECK (scope IN ('run', 'agent', 'all')),
		target            TEXT    NOT NULL,
		reason            TEXT    NOT NULL CHECK (reason <> ''),
		created_unix_nano INTEGER NOT NULL,
		PRIMARY KEY (scope, target),
		CHECK ((scope = 'all') = (target = ''))
	) WITHOUT ROWID;`,
}

// Store is the control database. It is safe for concurrent use, including by
// several processes on the same file.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock used for created times (default time.Now).
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// Open opens (creating if needed) the control database at path and brings
// its schema up to date. "" and ":memory:" are refused.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("controlplane: Open needs a file path, got %q", path)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate") // take the write lock at BEGIN
	db, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("controlplane: open %s: %w", path, err)
	}
	s := &Store{db: db, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("controlplane: migrate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("controlplane: read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("%w: database is v%d, this binary knows up to v%d",
			ErrSchemaTooNew, version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("controlplane: migration %d: %w", i+1, err)
		}
	}
	// PRAGMA does not accept bound parameters; len(migrations) is a constant.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations))); err != nil {
		return fmt.Errorf("controlplane: set schema version: %w", err)
	}
	return tx.Commit()
}
