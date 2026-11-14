package cli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

// closedStore returns an event log whose Append always fails, and its path.
func closedStore(t *testing.T) (*eventlog.Store, string) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "events.db")
	store, err := eventlog.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return store, db
}

// unrecordedSuspend suspends a fresh sandbox of run r1 into a store that
// cannot record the event.
func unrecordedSuspend(t *testing.T, b *sandboxtest.Pausable) (string, error) {
	t.Helper()
	b.Workspace = workspaceTar(t)
	spec := sandbox.Spec{RunID: "r1", Image: "img"}
	h, err := b.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	store, db := closedStore(t)
	s := &runSandbox{backend: b, handle: h, runID: "r1", suspends: true, spec: spec}
	var stdout bytes.Buffer
	err = s.suspend(context.Background(), runOptions{db: db}, store, &eventlog.RunState{LastSeq: 4}, &stdout)
	return stdout.String(), err
}

func TestSuspendRestoresWhenRecordFails(t *testing.T) {
	b := &sandboxtest.Pausable{}
	out, err := unrecordedSuspend(t, b)
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if live := b.Live(); len(live) != 1 {
		t.Fatalf("live sandboxes = %d, want 1 (restored)", len(live))
	}
	if !strings.Contains(out, "sandbox: pause not recorded") || !strings.Contains(out, "kept it for resume") {
		t.Errorf("stdout = %q", out)
	}
}

func TestSuspendNamesTarWhenRecordAndRestoreFail(t *testing.T) {
	b := &sandboxtest.Pausable{RestoreErr: errors.New("no image")}
	_, err := unrecordedSuspend(t, b)
	if err == nil {
		t.Fatal("suspend succeeded, want error")
	}
	if !strings.Contains(err.Error(), "5.tar") || !strings.Contains(err.Error(), "tar -xf") {
		t.Errorf("error = %v, want tar path and recovery command", err)
	}
}
