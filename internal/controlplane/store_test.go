package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T, opts ...Option) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "control.db"), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenCreatesSchema(t *testing.T) {
	s := openTemp(t)
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d (err %v), want %d", v, err, len(migrations))
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'kills'`).Scan(&name); err != nil {
		t.Fatalf("no kills table: %v", err)
	}
}

func TestOpenRejectsMemoryPath(t *testing.T) {
	for _, p := range []string{"", ":memory:"} {
		_, err := Open(context.Background(), p)
		if err == nil || !contains(err.Error(), "Open needs a file path") {
			t.Errorf("Open(%q) err = %v", p, err)
		}
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
}

func TestReopenKeepsKills(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Kill(ctx, ScopeAll, "", "x"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Active(ctx)
	if err != nil || len(got) != 1 || got[0].Scope != ScopeAll {
		t.Fatalf("Active = %v, %v", got, err)
	}
}
