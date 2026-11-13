package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/controlplane"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

func execCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Execute(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func controlPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "control.db")
}

func activeKills(t *testing.T, path string) []controlplane.Kill {
	t.Helper()
	s, err := controlplane.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestKillCommandSetsKill(t *testing.T) {
	cdb := controlPath(t)
	code, out, errOut := execCLI(t, "kill", "run", "r-1", "--reason", "maintenance", "--control-db", cdb)
	if code != 0 || out != "kill set: run r-1 (reason: maintenance)\n" {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	got := activeKills(t, cdb)
	if len(got) != 1 || got[0].Scope != controlplane.ScopeRun || got[0].Target != "r-1" || got[0].Reason != "maintenance" {
		t.Fatalf("Active = %+v", got)
	}
}

func TestKillCommandNeedsReason(t *testing.T) {
	code, _, errOut := execCLI(t, "kill", "all", "--control-db", controlPath(t))
	if code != 1 || !strings.Contains(errOut, `required flag(s) "reason" not set`) {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestKillCommandBadArgs(t *testing.T) {
	tests := [][]string{{}, {"run"}, {"all", "x"}, {"nope", "x"}}
	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			full := append([]string{"kill"}, args...)
			full = append(full, "--reason", "r", "--control-db", controlPath(t))
			code, _, errOut := execCLI(t, full...)
			if code != 1 || !strings.Contains(errOut, `kill: want "run RUN_ID", "agent AGENT" or "all"`) {
				t.Fatalf("code %d stderr %q", code, errOut)
			}
		})
	}
}

func TestKillCommandInvalidAgent(t *testing.T) {
	code, _, errOut := execCLI(t, "kill", "agent", "Bad", "--reason", "r", "--control-db", controlPath(t))
	if code != 1 || !strings.Contains(errOut, `invalid agent "Bad" (want NAME or NAME@VERSION)`) {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestUnkillCommand(t *testing.T) {
	cdb := controlPath(t)
	if code, _, e := execCLI(t, "kill", "run", "r-1", "--reason", "x", "--control-db", cdb); code != 0 {
		t.Fatal(e)
	}
	code, out, errOut := execCLI(t, "unkill", "run", "r-1", "--control-db", cdb)
	if code != 0 || out != "kill removed: run r-1\n" {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	code, _, errOut = execCLI(t, "unkill", "run", "r-1", "--control-db", cdb)
	if code != 1 || !strings.Contains(errOut, "no such kill: run r-1") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestUnkillMissingDB(t *testing.T) {
	cdb := controlPath(t)
	code, _, errOut := execCLI(t, "unkill", "all", "--control-db", cdb)
	if code != 1 || !strings.Contains(errOut, "control db "+cdb+" does not exist") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestKillsListing(t *testing.T) {
	cdb := controlPath(t)
	execCLI(t, "kill", "run", "r-1", "--reason", "x", "--control-db", cdb)
	execCLI(t, "kill", "all", "--reason", "y", "--control-db", cdb)
	code, out, errOut := execCLI(t, "kills", "--control-db", cdb)
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %q", out)
	}
	if got := strings.Fields(lines[0]); !reflect.DeepEqual(got, []string{"SCOPE", "TARGET", "REASON", "SINCE"}) {
		t.Fatalf("header = %v", got)
	}
	seen := map[string]bool{}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		if len(f) != 4 {
			t.Fatalf("row = %q", l)
		}
		seen[f[0]+" "+f[1]+" "+f[2]] = true
	}
	if !seen["run r-1 x"] || !seen["all - y"] {
		t.Fatalf("rows = %q", out)
	}
	// empty db
	cdb2 := controlPath(t)
	execCLI(t, "kill", "all", "--reason", "y", "--control-db", cdb2)
	execCLI(t, "unkill", "all", "--control-db", cdb2)
	if code, out, _ := execCLI(t, "kills", "--control-db", cdb2); code != 0 || out != "no active kills\n" {
		t.Fatalf("code %d stdout %q", code, out)
	}
}

func TestKillsMissingDBDoesNotCreateIt(t *testing.T) {
	cdb := controlPath(t)
	code, out, _ := execCLI(t, "kills", "--control-db", cdb)
	if code != 0 || out != "no active kills\n" {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if _, err := os.Stat(cdb); !os.IsNotExist(err) {
		t.Fatalf("control db exists: %v", err)
	}
}

func hasKind(t *testing.T, db, runID, kind string) bool {
	t.Helper()
	s, err := eventlog.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evs, err := s.Read(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if eventlog.Kind(e) == kind {
			return true
		}
	}
	return false
}

func TestRunStopsWhenAgentKilled(t *testing.T) {
	agent, script, db := helloDir(t, "")
	cdb := controlPath(t)
	if code, _, e := execCLI(t, "kill", "agent", "hello", "--reason", "frozen", "--control-db", cdb); code != 0 {
		t.Fatal(e)
	}
	code, out, errOut := execCLI(t, "run", agent, "--task", "t", "--db", db, "--fake-script", script,
		"--run-id", "r1", "--control-db", cdb)
	if code != 2 || !strings.Contains(out, "stop:    cancelled\n") || !strings.Contains(out, "detail:  killed: frozen\n") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	if hasKind(t, db, "r1", "model_call_started") {
		t.Fatal("log has model_call_started")
	}
}

func TestRunWithoutControlDBDoesNotCreateIt(t *testing.T) {
	agent, script, db := helloDir(t, "")
	cdb := controlPath(t)
	code, _, errOut := execCLI(t, "run", agent, "--task", "t", "--db", db, "--fake-script", script, "--control-db", cdb)
	if code != 0 {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if _, err := os.Stat(cdb); !os.IsNotExist(err) {
		t.Fatalf("control db exists: %v", err)
	}
}

func TestResumeStopsWhenRunKilled(t *testing.T) {
	agent, script, db := helloDir(t, "")
	crashedRun(t, agent, script, db)
	cdb := controlPath(t)
	if code, _, e := execCLI(t, "kill", "run", "r1", "--reason", "x", "--control-db", cdb); code != 0 {
		t.Fatal(e)
	}
	code, out, errOut := execCLI(t, "resume", "r1", agent, "--db", db, "--fake-script", script, "--control-db", cdb)
	if code != 2 || !strings.Contains(out, "detail:  killed: x\n") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestApproveStopsWhenRunKilled(t *testing.T) {
	e := pausedRun(t)
	cdb := controlPath(t)
	if code, _, errOut := execCLI(t, "kill", "run", "r1", "--reason", "too risky", "--control-db", cdb); code != 0 {
		t.Fatal(errOut)
	}
	code, out, errOut := execCLI(t, "approve", "a-call_1", "r1", e.agent, "--fake-script", e.approveScript,
		"--db", e.db, "--by", "bob", "--control-db", cdb)
	if code != 2 || !strings.Contains(out, "detail:  killed: too risky\n") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	if _, err := os.Stat(e.workspaceFile()); !os.IsNotExist(err) {
		t.Fatalf("approved call wrote out.txt after the run was killed (%v)", err)
	}
}
