// Package eventlog is the append-only run event log, the framework's source
// of truth. Run state, traces, cost reports and evaluations are all derived
// from it (see docs/design, "the event log is the source of truth").
//
// Events are stored in SQLite as deterministic protobuf encodings of
// adlcv1.Event. Each run's events are numbered 1, 2, 3, ... with no gaps;
// Append assigns the number. Rows can never be updated or deleted: the
// schema rejects it with triggers, so the guarantee holds even for code that
// bypasses this package.
package eventlog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

var (
	// ErrInvalidEvent means an event was rejected before it was written.
	ErrInvalidEvent = errors.New("eventlog: invalid event")
	// ErrSeqConflict means the caller asked for a seq that is not the run's
	// next one, i.e. someone else appended first.
	ErrSeqConflict = errors.New("eventlog: seq conflict")
	// ErrRunNotFound means the run has no events.
	ErrRunNotFound = errors.New("eventlog: run not found")
	// ErrSchemaTooNew means the database was written by a newer version.
	ErrSchemaTooNew = errors.New("eventlog: database schema is newer than this binary")
)

// migrations[i] moves the schema from version i to i+1. Never edit an
// existing entry; append a new one.
var migrations = []string{
	`CREATE TABLE events (
		run_id         TEXT    NOT NULL CHECK (run_id <> ''),
		seq            INTEGER NOT NULL CHECK (seq > 0),
		time_unix_nano INTEGER NOT NULL,
		kind           TEXT    NOT NULL,
		data           BLOB    NOT NULL,
		PRIMARY KEY (run_id, seq)
	) WITHOUT ROWID;
	CREATE TRIGGER events_no_update BEFORE UPDATE ON events
	BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
	CREATE TRIGGER events_no_delete BEFORE DELETE ON events
	BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;`,
}

// Store is an event log backed by one SQLite file. It is safe for
// concurrent use, including by several processes on the same file.
type Store struct {
	db  *sql.DB
	now func() time.Time
	// mu serializes appends within this process; SQLite's write lock
	// (taken at BEGIN, see _txlock below) serializes across processes.
	mu sync.Mutex
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock used to timestamp events that have no time.
func WithClock(now func() time.Time) Option {
	return func(s *Store) { s.now = now }
}

// Open opens (creating if needed) the event log at path and brings its
// schema up to date. path must be a file; in-memory databases are not
// supported because each pooled connection would see a different database.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("eventlog: Open needs a file path, got %q", path)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate") // take the write lock at BEGIN
	db, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
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

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("eventlog: migrate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var version int
	if err := tx.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("eventlog: read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("%w: database is v%d, this binary knows up to v%d",
			ErrSchemaTooNew, version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("eventlog: migration %d: %w", i+1, err)
		}
	}
	// PRAGMA does not accept bound parameters; len(migrations) is a constant.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations))); err != nil {
		return fmt.Errorf("eventlog: set schema version: %w", err)
	}
	return tx.Commit()
}

// Append writes ev as the next event of its run and returns the stored copy,
// with Seq (and Time, if unset) filled in. ev itself is not modified.
//
// If ev.Seq is non-zero it must equal the run's next seq, otherwise
// ErrSeqConflict is returned and nothing is written. This lets a writer
// detect that another writer appended to the same run in between.
func (s *Store) Append(ctx context.Context, ev *adlcv1.Event) (*adlcv1.Event, error) {
	if ev.GetRunId() == "" {
		return nil, fmt.Errorf("%w: run_id is empty", ErrInvalidEvent)
	}
	kind := Kind(ev)
	if kind == "" {
		return nil, fmt.Errorf("%w: no payload", ErrInvalidEvent)
	}
	ev = proto.CloneOf(ev)

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("eventlog: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var last int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM events WHERE run_id = ?`, ev.GetRunId(),
	).Scan(&last); err != nil {
		return nil, fmt.Errorf("eventlog: read last seq: %w", err)
	}
	next := uint64(last) + 1 //nolint:gosec // seq is CHECKed > 0, so last >= 0

	if ev.GetSeq() != 0 && ev.GetSeq() != next {
		return nil, fmt.Errorf("%w: run %q: asked for seq %d, next is %d",
			ErrSeqConflict, ev.GetRunId(), ev.GetSeq(), next)
	}
	if next == 1 && kind != kindRunStarted {
		return nil, fmt.Errorf("%w: first event of run %q must be run_started, got %s",
			ErrInvalidEvent, ev.GetRunId(), kind)
	}
	if next != 1 && kind == kindRunStarted {
		return nil, fmt.Errorf("%w: run %q already started", ErrInvalidEvent, ev.GetRunId())
	}
	if ev.GetParentSeq() >= next {
		return nil, fmt.Errorf("%w: parent_seq %d must be before seq %d",
			ErrInvalidEvent, ev.GetParentSeq(), next)
	}
	ev.Seq = next
	if ev.GetTime() == nil {
		ev.Time = timestamppb.New(s.now())
	}

	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("eventlog: encode: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO events (run_id, seq, time_unix_nano, kind, data) VALUES (?, ?, ?, ?, ?)`,
		ev.GetRunId(), int64(next), ev.GetTime().AsTime().UnixNano(), kind, data, //nolint:gosec // seq fits in int64
	); err != nil {
		return nil, fmt.Errorf("eventlog: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("eventlog: commit: %w", err)
	}
	return ev, nil
}

// Read returns the run's events with seq > afterSeq, in seq order.
// Read(ctx, id, 0) returns the whole run. An unknown run returns no events
// and no error.
func (s *Store) Read(ctx context.Context, runID string, afterSeq uint64) ([]*adlcv1.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT data FROM events WHERE run_id = ? AND seq > ? ORDER BY seq`,
		runID, int64(afterSeq)) //nolint:gosec // seq fits in int64
	if err != nil {
		return nil, fmt.Errorf("eventlog: read: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*adlcv1.Event
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("eventlog: scan: %w", err)
		}
		ev := &adlcv1.Event{}
		if err := proto.Unmarshal(data, ev); err != nil {
			return nil, fmt.Errorf("eventlog: decode run %q: %w", runID, err)
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventlog: read: %w", err)
	}
	return out, nil
}

// State folds the run's events into its current state.
func (s *Store) State(ctx context.Context, runID string) (*RunState, error) {
	events, err := s.Read(ctx, runID, 0)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrRunNotFound, runID)
	}
	return Fold(events)
}

const kindRunStarted = "run_started"

// Kind returns the name of ev's payload kind (e.g. "tool_call_requested"),
// or "" if it has none or one this binary does not know.
func Kind(ev *adlcv1.Event) string {
	m := ev.ProtoReflect()
	f := m.WhichOneof(m.Descriptor().Oneofs().ByName("payload"))
	if f == nil {
		return ""
	}
	return string(f.Name())
}
