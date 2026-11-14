package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

func TestApproveVersionMismatchRefusesBeforeRestore(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	yml, err := os.ReadFile(e.agent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.agent, []byte(strings.Replace(string(yml), "version: 1", "version: 2", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	before := len(runEvents(t, e.db))
	_, err = approveWith(t, e, "a-call_1", approveScript)
	if !errors.Is(err, harness.ErrAgentMismatch) {
		t.Fatalf("approve err = %v, want ErrAgentMismatch", err)
	}
	evs := runEvents(t, e.db)
	if len(evs) != before || len(restoredSeqs(evs)) != 0 {
		t.Errorf("events %d -> %d, restored %v; want nothing appended", before, len(evs), restoredSeqs(evs))
	}
	if live := e.b.Live(); len(live) != 0 {
		t.Errorf("live sandboxes %v, want none (no restore)", live)
	}
	if tars, _ := filepath.Glob(filepath.Join(snapshotDir(e), "*.tar")); len(tars) != 1 {
		t.Errorf("tars = %v, want the snapshot untouched", tars)
	}
}

func TestCheckAgentVersion(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	store, err := eventlog.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tests := []struct {
		name    string
		version int
		wantErr bool
	}{
		{"same version", 1, false},
		{"other version", 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &agentconfig.Agent{Name: "hello", Version: tt.version}
			err := checkAgentVersion(context.Background(), store, "r1", a)
			if got := errors.Is(err, harness.ErrAgentMismatch); got != tt.wantErr {
				t.Errorf("err = %v, want mismatch %v", err, tt.wantErr)
			}
		})
	}
}
