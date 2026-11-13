package eventlog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func fixedClock() time.Time { return t0 }

func openStore(t *testing.T, path string) *Store {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "events.db")
	}
	s, err := Open(context.Background(), path, WithClock(fixedClock))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func started(runID string) *adlcv1.Event {
	return &adlcv1.Event{RunId: runID, Payload: &adlcv1.Event_RunStarted{
		RunStarted: &adlcv1.RunStarted{AgentVersion: "coder@1", Task: "fix the bug"},
	}}
}

func errorEvent(runID, msg string) *adlcv1.Event {
	return &adlcv1.Event{RunId: runID, Payload: &adlcv1.Event_ErrorOccurred{
		ErrorOccurred: &adlcv1.ErrorOccurred{Message: msg},
	}}
}

func mustAppend(t *testing.T, s *Store, ev *adlcv1.Event) *adlcv1.Event {
	t.Helper()
	out, err := s.Append(context.Background(), ev)
	if err != nil {
		t.Fatalf("Append(%s): %v", Kind(ev), err)
	}
	return out
}

func TestAppendAssignsSeqAndTime(t *testing.T) {
	s := openStore(t, "")
	first := mustAppend(t, s, started("r1"))
	second := mustAppend(t, s, errorEvent("r1", "x"))

	if first.GetSeq() != 1 || second.GetSeq() != 2 {
		t.Errorf("seqs = %d, %d; want 1, 2", first.GetSeq(), second.GetSeq())
	}
	if !first.GetTime().AsTime().Equal(t0) {
		t.Errorf("time = %v, want clock time %v", first.GetTime().AsTime(), t0)
	}
}

func TestAppendKeepsCallerTime(t *testing.T) {
	s := openStore(t, "")
	when := t0.Add(-time.Hour)
	ev := started("r1")
	ev.Time = timestamppb.New(when)
	if got := mustAppend(t, s, ev).GetTime().AsTime(); !got.Equal(when) {
		t.Errorf("time = %v, want caller's %v", got, when)
	}
}

func TestAppendDoesNotModifyInput(t *testing.T) {
	s := openStore(t, "")
	ev := started("r1")
	before := proto.CloneOf(ev)
	mustAppend(t, s, ev)
	if !proto.Equal(ev, before) {
		t.Errorf("Append modified its input: %v", ev)
	}
}

func TestAppendRejects(t *testing.T) {
	s := openStore(t, "")
	mustAppend(t, s, started("r1"))

	tests := []struct {
		name string
		ev   *adlcv1.Event
		want error
	}{
		{"empty run id", started(""), ErrInvalidEvent},
		{"no payload", &adlcv1.Event{RunId: "r1"}, ErrInvalidEvent},
		{"first event not run_started", errorEvent("r2", "x"), ErrInvalidEvent},
		{"second run_started", started("r1"), ErrInvalidEvent},
		{"parent_seq not before seq", func() *adlcv1.Event {
			ev := errorEvent("r1", "x")
			ev.ParentSeq = 2
			return ev
		}(), ErrInvalidEvent},
		{"seq conflict", func() *adlcv1.Event {
			ev := errorEvent("r1", "x")
			ev.Seq = 5
			return ev
		}(), ErrSeqConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Append(context.Background(), tt.ev)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	// Nothing rejected above was written.
	if evs, _ := s.Read(context.Background(), "r1", 0); len(evs) != 1 {
		t.Errorf("r1 has %d events after rejected appends, want 1", len(evs))
	}
	if evs, _ := s.Read(context.Background(), "r2", 0); len(evs) != 0 {
		t.Errorf("r2 has %d events, want 0", len(evs))
	}
}

func TestAppendWithExpectedSeq(t *testing.T) {
	s := openStore(t, "")
	ev := started("r1")
	ev.Seq = 1
	mustAppend(t, s, ev)
	next := errorEvent("r1", "x")
	next.Seq = 2
	if got := mustAppend(t, s, next).GetSeq(); got != 2 {
		t.Errorf("seq = %d, want 2", got)
	}
}

func TestReadOrderAndAfterSeq(t *testing.T) {
	s := openStore(t, "")
	in := []*adlcv1.Event{mustAppend(t, s, started("r1"))}
	for i := range 4 {
		in = append(in, mustAppend(t, s, errorEvent("r1", fmt.Sprint(i))))
	}

	all, err := s.Read(context.Background(), "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(in) {
		t.Fatalf("read %d events, want %d", len(all), len(in))
	}
	for i := range in {
		if !proto.Equal(all[i], in[i]) {
			t.Errorf("event %d differs:\n got %v\nwant %v", i, all[i], in[i])
		}
	}

	tail, err := s.Read(context.Background(), "r1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 2 || tail[0].GetSeq() != 4 || tail[1].GetSeq() != 5 {
		t.Errorf("Read after seq 3 = %v, want seqs 4, 5", seqs(tail))
	}

	none, err := s.Read(context.Background(), "no-such-run", 0)
	if err != nil || len(none) != 0 {
		t.Errorf("unknown run: %d events, err %v; want 0, nil", len(none), err)
	}
}

func TestRunsAreNumberedIndependently(t *testing.T) {
	s := openStore(t, "")
	mustAppend(t, s, started("a"))
	mustAppend(t, s, started("b"))
	if got := mustAppend(t, s, errorEvent("a", "x")).GetSeq(); got != 2 {
		t.Errorf("run a seq = %d, want 2", got)
	}
	if got := mustAppend(t, s, errorEvent("b", "x")).GetSeq(); got != 2 {
		t.Errorf("run b seq = %d, want 2", got)
	}
}

// concurrentAppend has `writers` goroutines append `each` events to every
// run, spread across the given stores, and checks that every run ends up
// numbered 1..N with no gaps or duplicates.
func concurrentAppend(t *testing.T, stores []*Store, runs []string, writers, each int) {
	t.Helper()
	for _, r := range runs {
		mustAppend(t, stores[0], started(r))
	}
	var wg sync.WaitGroup
	errs := make(chan error, writers*len(runs))
	for w := range writers {
		for _, r := range runs {
			wg.Add(1)
			go func(st *Store, run string) {
				defer wg.Done()
				for i := range each {
					if _, err := st.Append(context.Background(), errorEvent(run, fmt.Sprint(i))); err != nil {
						errs <- err
						return
					}
				}
			}(stores[w%len(stores)], r)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Append: %v", err)
	}

	want := 1 + writers*each
	for _, r := range runs {
		evs, err := stores[0].Read(context.Background(), r, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != want {
			t.Fatalf("run %s has %d events, want %d", r, len(evs), want)
		}
		for i, ev := range evs {
			if ev.GetSeq() != uint64(i+1) { //nolint:gosec // small test index
				t.Fatalf("run %s: event %d has seq %d", r, i, ev.GetSeq())
			}
		}
		if _, err := Fold(evs); err != nil {
			t.Fatalf("run %s does not fold: %v", r, err)
		}
	}
}

func TestConcurrentAppends(t *testing.T) {
	s := openStore(t, "")
	concurrentAppend(t, []*Store{s}, []string{"a", "b"}, 8, 25)
}

// Two Stores on one file behave like two processes: only SQLite's own
// locking keeps them from colliding.
func TestConcurrentAppendsFromTwoStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	a, b := openStore(t, path), openStore(t, path)
	concurrentAppend(t, []*Store{a, b}, []string{"r"}, 6, 20)
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s := openStore(t, path)
	in := mustAppend(t, s, started("r1"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := openStore(t, path)
	evs, err := s2.Read(context.Background(), "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || !proto.Equal(evs[0], in) {
		t.Fatalf("after reopen got %v, want [%v]", evs, in)
	}
	if got := mustAppend(t, s2, errorEvent("r1", "x")).GetSeq(); got != 2 {
		t.Errorf("seq after reopen = %d, want 2", got)
	}
}

func TestRowsCannotBeUpdatedOrDeleted(t *testing.T) {
	s := openStore(t, "")
	mustAppend(t, s, started("r1"))
	ctx := context.Background()
	for _, stmt := range []string{
		`UPDATE events SET kind = 'x'`,
		`DELETE FROM events`,
	} {
		_, err := s.db.ExecContext(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%q: err = %v, want append-only error", stmt, err)
		}
	}
	if evs, _ := s.Read(ctx, "r1", 0); len(evs) != 1 {
		t.Errorf("events changed: %d left, want 1", len(evs))
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s := openStore(t, path)
	if _, err := s.db.ExecContext(context.Background(), `PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	_, err := Open(context.Background(), path)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	_ = openStore(t, path).Close()
	_ = openStore(t, path) // migrations must not run twice
}

func TestOpenRejectsMemory(t *testing.T) {
	for _, p := range []string{"", ":memory:"} {
		if _, err := Open(context.Background(), p); err == nil {
			t.Errorf("Open(%q) succeeded, want error", p)
		}
	}
}

func TestState(t *testing.T) {
	s := openStore(t, "")
	ctx := context.Background()
	if _, err := s.State(ctx, "r1"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("err = %v, want ErrRunNotFound", err)
	}
	mustAppend(t, s, started("r1"))
	mustAppend(t, s, errorEvent("r1", "x"))
	st, err := s.State(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if st.LastSeq != 2 || st.Errors != 1 || st.Status != StatusRunning {
		t.Errorf("state = seq %d errors %d status %s; want 2, 1, running", st.LastSeq, st.Errors, st.Status)
	}
}

func seqs(evs []*adlcv1.Event) []uint64 {
	out := make([]uint64, len(evs))
	for i, ev := range evs {
		out[i] = ev.GetSeq()
	}
	return out
}
