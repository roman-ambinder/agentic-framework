package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

const (
	approvalRunScript = `{"turns": [{"text": "Writing.", "tool_calls": [{"id": "call_1", "name": "fs__write_file", "arguments": {"path":"out.txt","content":"hi"}}], "finish": "tool_use", "usage": {"input_tokens": 1, "output_tokens": 1}}]}`
	approveScript     = `{"turns": [{"text": "Done.", "finish": "end_turn", "usage": {"input_tokens": 1, "output_tokens": 1}}]}`
	writesRule        = "    - id: writes\n      risk: write_local\n      decision: needs_approval\n"
)

// approvalEnv is a directory with a run "r1" paused on approval a-call_1.
type approvalEnv struct {
	dir, agent, approveScript, db string
}

func (e approvalEnv) workspaceFile() string { return filepath.Join(e.dir, "workspace", "out.txt") }

// pausedRun runs the hello agent (with a writes rule) until it waits for approval.
func pausedRun(t *testing.T) approvalEnv {
	t.Helper()
	agent, script, db := helloDir(t, approvalRunScript)
	dir := filepath.Dir(agent)
	yml, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(yml), "./workspace", filepath.Join(dir, "workspace"), 1)
	s = strings.Replace(s, "      decision: allow\n", "      decision: allow\n"+writesRule, 1)
	if err := os.WriteFile(agent, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	ap := filepath.Join(dir, "approve.json")
	if err := os.WriteFile(ap, []byte(approveScript), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fixed := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	store, err := eventlog.Open(ctx, db, eventlog.WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatal(err)
	}
	o := runOptions{agentPath: agent, task: "write it", db: db, fakeScript: script, runID: "r1"}
	a, err := loadAgent(o)
	if err != nil {
		t.Fatal(err)
	}
	cfg, deps, cleanup, err := buildRun(ctx, a, o, "r1", store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	st, err := harness.Run(ctx, cfg, deps)
	if err != nil || st.Status != eventlog.StatusPaused {
		t.Fatalf("setup: %v, %+v", err, st)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return approvalEnv{dir: dir, agent: agent, approveScript: ap, db: db}
}

func runCLI(args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = Execute(args, &o, &e)
	return code, o.String(), e.String()
}

func eventCount(t *testing.T, db string) int {
	t.Helper()
	store, err := eventlog.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	evs, err := store.Read(context.Background(), "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(evs)
}

func TestApprovalsListsPending(t *testing.T) {
	e := pausedRun(t)
	code, out, errOut := runCLI("approvals", "--db", e.db)
	want := `APPROVAL  RUN  CALL    AGENT    REQUESTED             SUMMARY
a-call_1  r1   call_1  hello@1  2026-11-13T10:00:00Z  fs__write_file {"path":"out.txt","content":"hi"}
`
	if code != 0 || out != want {
		t.Fatalf("code %d, stderr %q, stdout:\n%s\nwant:\n%s", code, errOut, out, want)
	}
}

func TestApprovalsNone(t *testing.T) {
	agent, script, db := helloDir(t, "")
	if code, _, e := runCLI("run", agent, "--task", "t", "--db", db, "--fake-script", script); code != 0 {
		t.Fatalf("setup: %d %s", code, e)
	}
	code, out, _ := runCLI("approvals", "--db", db)
	if code != 0 || out != "no pending approvals\n" {
		t.Fatalf("code %d, stdout %q", code, out)
	}
}

func TestApproveCommandCompletesRun(t *testing.T) {
	e := pausedRun(t)
	code, out, errOut := runCLI("approve", "a-call_1", "r1", e.agent, "--fake-script", e.approveScript, "--db", e.db, "--by", "bob")
	if code != 0 || !strings.Contains(out, "stop:    completed") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if b, err := os.ReadFile(e.workspaceFile()); err != nil || string(b) != "hi" {
		t.Fatalf("out.txt = %q, %v", b, err)
	}
	if _, out, _ := runCLI("approvals", "--db", e.db); !strings.Contains(out, "no pending approvals") {
		t.Fatalf("approvals = %q", out)
	}
}

func TestApproveCommandRejectDoesNotWrite(t *testing.T) {
	e := pausedRun(t)
	code, _, errOut := runCLI("approve", "a-call_1", "r1", e.agent, "--fake-script", e.approveScript, "--db", e.db,
		"--by", "bob", "--reject", "--comment", "no")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(e.workspaceFile()); !os.IsNotExist(err) {
		t.Fatalf("out.txt exists (%v)", err)
	}
	store, err := eventlog.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	evs, _ := store.Read(context.Background(), "r1", 0)
	var found bool
	for _, c := range pick(evs, (*adlcv1.Event).GetToolCallCompleted) {
		found = found || string(c.GetOutput()) == "rejected by bob: no"
	}
	if !found {
		t.Fatal("no tool_call_completed with the rejection")
	}
}

func TestApproveCommandNeedsAgentFile(t *testing.T) {
	e := pausedRun(t)
	n := eventCount(t, e.db)
	code, _, errOut := runCLI("approve", "a-call_1", "r1", "--db", e.db)
	if code != 1 || !strings.Contains(errOut, `AGENT_YAML is required to continue run "r1"`) {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if eventCount(t, e.db) != n {
		t.Fatal("events were appended")
	}
}

func TestApproveCommandUnknownApproval(t *testing.T) {
	e := pausedRun(t)
	code, _, errOut := runCLI("approve", "a-x", "r1", e.agent, "--fake-script", e.approveScript, "--db", e.db)
	if code != 1 || !strings.Contains(errOut, "unknown approval") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestApproverName(t *testing.T) {
	env := func(u string) func(string) string {
		return func(k string) string {
			if k == "USER" {
				return u
			}
			return ""
		}
	}
	tests := []struct{ name, flag, user, want string }{
		{"flag wins", "ann", "bob", "ann"},
		{"user", "", "bob", "bob"},
		{"local", "", "", "local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approverName(tt.flag, env(tt.user)); got != tt.want {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestOneLine(t *testing.T) {
	long := strings.Repeat("é", 90)
	tests := []struct{ name, in, want string }{
		{"breaks and tabs", "a\nb\r\nc\td", "a b  c d"},
		{"exactly 80", strings.Repeat("x", 80), strings.Repeat("x", 80)},
		{"truncated by runes", long, strings.Repeat("é", 77) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := oneLine(tt.in); got != tt.want {
				t.Fatalf("got %q", got)
			}
		})
	}
}

// restoredSeqs returns suspended_seq of every sandbox_restored event.
func restoredSeqs(evs []*adlcv1.Event) []uint64 {
	var out []uint64
	for _, r := range pick(evs, (*adlcv1.Event).GetSandboxRestored) {
		out = append(out, r.GetSuspendedSeq())
	}
	return out
}

func TestApproveRestoresLatestSnapshot(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	out, err := approveWith(t, e, "a-call_1", parkAgainScript)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("first approve: err = %v, stdout %q", err, out)
	}
	if !strings.Contains(out, "sandbox: restored fake-2 (files only)\n") || !strings.Contains(out, "sandbox: paused (files only: ") {
		t.Errorf("first approve stdout = %q", out)
	}
	out, err = approveWith(t, e, "a-call_2", approveScript)
	if err != nil || !strings.Contains(out, "stop:    completed") || !strings.Contains(out, "sandbox: restored fake-3 (files only)\n") {
		t.Fatalf("second approve: err = %v, stdout %q", err, out)
	}
	evs := runEvents(t, e.db)
	suspended, restored := suspendedSeqs(evs), restoredSeqs(evs)
	if len(suspended) != 2 || len(restored) != 2 || restored[0] != suspended[0] || restored[1] != suspended[1] {
		t.Fatalf("suspended %v, restored %v; want each restore to name the latest pause", suspended, restored)
	}
	if live := e.b.Live(); len(live) != 0 {
		t.Errorf("live sandboxes %v, want none", live)
	}
	if got, err := os.ReadFile(filepath.Join(e.dir, "out", "greeting.txt")); err != nil || string(got) != "hello, world\n" {
		t.Errorf("copied-out greeting.txt = %q, %v", got, err)
	}
}

func TestApproveRunWithoutSnapshotUsesLiveContainer(t *testing.T) {
	agent, script, db := helloDir(t, approvalRunScript)
	dir := filepath.Dir(agent)
	// The fs root names the sandbox, so the agent needs the live lookup.
	if err := os.MkdirAll(filepath.Join(dir, "workspace", "fake-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	yml := strings.Replace(pausableAgentYAML(dir, "  pause: keep\n"), "/workspace\"]", "/workspace/{{sandbox_id}}\"]", 1)
	e := pausableRunAgent(t, agent, yml, script, db, &sandboxtest.Pausable{})
	if live := e.b.Live(); len(live) != 1 || len(suspendedSeqs(runEvents(t, db))) != 0 {
		t.Fatalf("setup: live %v, stdout %q", live, e.stdout)
	}
	out, err := approveWith(t, e, "a-call_1", approveScript)
	if err != nil || !strings.Contains(out, "stop:    completed") {
		t.Fatalf("err = %v, stdout %q", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "workspace", "fake-1", "out.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("out.txt = %q, %v (tools did not use the live sandbox)", b, err)
	}
	if n := len(restoredSeqs(runEvents(t, db))); n != 0 {
		t.Errorf("%d sandbox_restored events, want none", n)
	}
	if live := e.b.Live(); len(live) != 0 {
		t.Errorf("live sandboxes %v, want the completed run's sandbox destroyed", live)
	}
}

func TestApproveFailsWhenSnapshotMissing(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	seqs := suspendedSeqs(runEvents(t, e.db))
	if len(seqs) != 1 {
		t.Fatalf("setup: sandbox_suspended at %v", seqs)
	}
	tarPath := snapshotPath(e.db, "r1", seqs[0])
	if err := os.Remove(tarPath); err != nil {
		t.Fatal(err)
	}
	n := eventCount(t, e.db)
	_, err := approveWith(t, e, "a-call_1", approveScript)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 || !strings.Contains(err.Error(), "resume: restore sandbox from "+tarPath+": ") {
		t.Fatalf("err = %v", err)
	}
	if got := eventCount(t, e.db); got != n {
		t.Errorf("%d events after the failed approve, want %d", got, n)
	}
	if live := e.b.Live(); len(live) != 0 {
		t.Errorf("live sandboxes %v, want none", live)
	}
}

func TestApproveFailureAfterRestorePausesAgain(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	yml, err := os.ReadFile(e.agent)
	if err != nil {
		t.Fatal(err)
	}
	// A different agent version passes the restore but makes harness.Approve fail.
	if err := os.WriteFile(e.agent, []byte(strings.Replace(string(yml), "version: 1", "version: 2", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := approveWith(t, e, "a-call_1", approveScript)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 || !errors.Is(err, harness.ErrAgentMismatch) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "sandbox: restored fake-2 (files only)\n") || !strings.Contains(out, "sandbox: paused (files only: ") {
		t.Errorf("stdout = %q", out)
	}
	evs := runEvents(t, e.db)
	if s, r := suspendedSeqs(evs), restoredSeqs(evs); len(s) != 2 || len(r) != 1 || r[0] != s[0] {
		t.Fatalf("suspended %v, restored %v", s, r)
	}
	if live := e.b.Live(); len(live) != 0 {
		t.Errorf("live sandboxes %v, want none", live)
	}
}
