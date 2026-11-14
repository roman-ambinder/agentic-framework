package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

// snapshotDir is where run r1's snapshot tars live for env e.
func snapshotDir(e pausableEnv) string { return filepath.Join(filepath.Dir(e.db), "snapshots", "r1") }

func TestFinishRemovesSnapshotsWhenRunCompletes(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	if out, err := approveWith(t, e, "a-call_1", approveScript); err != nil {
		t.Fatalf("approve: %v, stdout %q", err, out)
	}
	if _, err := os.Stat(snapshotDir(e)); !os.IsNotExist(err) {
		t.Errorf("snapshot directory still there after the run completed (stat err %v)", err)
	}
}

func TestFinishKeepsSnapshotsWhileAwaitingApproval(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	if _, err := approveWith(t, e, "a-call_1", parkAgainScript); err == nil {
		t.Fatal("approve: want the run to park again (exit 2)")
	}
	tars, err := filepath.Glob(filepath.Join(snapshotDir(e), "*.tar"))
	if err != nil || len(tars) != 2 {
		t.Errorf("tars = %v, %v; want both pauses kept while the run waits", tars, err)
	}
}

func TestFinishKeepsSnapshotsWhenCopyOutFails(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	e.b.Workspace = []byte("not a tar archive")
	if _, err := approveWith(t, e, "a-call_1", approveScript); err == nil {
		t.Fatal("approve: want the workspace copy to fail")
	}
	if tars, _ := filepath.Glob(filepath.Join(snapshotDir(e), "*.tar")); len(tars) != 1 {
		t.Errorf("tars = %v; want the snapshot kept when the copy-out failed", tars)
	}
}
