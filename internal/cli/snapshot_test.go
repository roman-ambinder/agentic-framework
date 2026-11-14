package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

// parkAgainScript asks for a second write, so the approved run parks again.
const parkAgainScript = `{"turns": [{"text": "Writing again.", "tool_calls": [{"id": "call_2", "name": "fs__write_file", "arguments": {"path":"out2.txt","content":"again"}}], "finish": "tool_use", "usage": {"input_tokens": 1, "output_tokens": 1}}]}`

// pausableEnv is run "r1" of the hello agent with a sandbox section, parked
// on approval a-call_1, with b as its sandbox backend.
type pausableEnv struct {
	approvalEnv
	b      *sandboxtest.Pausable
	stdout string
	err    error
}

// workspaceTar returns a /workspace archive with greeting.txt, as CopyOut
// of a real backend returns it.
func workspaceTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("hello, world\n")
	if err := tw.WriteHeader(&tar.Header{Name: "workspace/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "workspace/greeting.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pausableAgentYAML is the hello agent with the writes rule and a sandbox
// section; sandboxLines are appended under `sandbox:`.
func pausableAgentYAML(dir, sandboxLines string) string {
	s := strings.Replace(helloYAML, "./workspace", filepath.Join(dir, "workspace"), 1)
	s = strings.Replace(s, "      decision: allow\n", "      decision: allow\n"+writesRule, 1)
	return s + "sandbox:\n  workspace: " + filepath.Join(dir, "workspace") + "\n" + sandboxLines
}

// pausableRun runs the agent until it waits for approval; b is the backend.
func pausableRun(t *testing.T, sandboxLines string, b *sandboxtest.Pausable) pausableEnv {
	t.Helper()
	agent, script, db := helloDir(t, approvalRunScript)
	dir := filepath.Dir(agent)
	return pausableRunAgent(t, agent, pausableAgentYAML(dir, sandboxLines), script, db, b)
}

func pausableRunAgent(t *testing.T, agent, yml, script, db string, b *sandboxtest.Pausable) pausableEnv {
	t.Helper()
	if err := os.WriteFile(agent, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	if b.Workspace == nil {
		b.Workspace = workspaceTar(t)
	}
	dir := filepath.Dir(agent)
	o := runOptions{agentPath: agent, task: "write it", db: db, fakeScript: script, runID: "r1", workspaceOut: filepath.Join(dir, "out")}
	var stdout bytes.Buffer
	err := runAgentWith(context.Background(), o, &stdout, func(context.Context) (runBackend, error) { return b, nil })
	return pausableEnv{approvalEnv: approvalEnv{dir: dir, agent: agent, db: db}, b: b, stdout: stdout.String(), err: err}
}

// approveWith answers approvalID of run r1 with the fake model script and b.
func approveWith(t *testing.T, e pausableEnv, approvalID, script string) (string, error) {
	t.Helper()
	p := filepath.Join(e.dir, approvalID+".json")
	if err := os.WriteFile(p, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	o := approveOptions{runOptions: runOptions{agentPath: e.agent, db: e.db, fakeScript: p, workspaceOut: filepath.Join(e.dir, "out")}, by: "bob"}
	var stdout bytes.Buffer
	err := approveRun(context.Background(), o, approvalID, "r1", &stdout,
		func(context.Context) (sandboxLookup, error) { return e.b, nil })
	return stdout.String(), err
}

// runEvents returns every event of run r1.
func runEvents(t *testing.T, db string) []*adlcv1.Event {
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
	return evs
}

// suspendedSeqs returns the seqs of the run's sandbox_suspended events.
func suspendedSeqs(evs []*adlcv1.Event) []uint64 {
	var out []uint64
	for _, ev := range evs {
		if ev.GetSandboxSuspended() != nil {
			out = append(out, ev.GetSeq())
		}
	}
	return out
}

func TestSnapshotPath(t *testing.T) {
	got := snapshotPath(filepath.Join("x", ".adlc", "events.db"), "r1", 7)
	if want := filepath.Join("x", ".adlc", "snapshots", "r1", "7.tar"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func suspendedEvent(ref string) *adlcv1.Event {
	return &adlcv1.Event{RunId: "r1", Payload: &adlcv1.Event_SandboxSuspended{SandboxSuspended: &adlcv1.SandboxSuspended{
		SnapshotRef: ref, WorkspaceSha256: "sha256:" + ref, WorkspaceBytes: 3, ImageId: "img", ProcessesSkipped: sandbox.ProcessesNotSupported,
	}}}
}

func restoredEvent(seq uint64) *adlcv1.Event {
	return &adlcv1.Event{RunId: "r1", Payload: &adlcv1.Event_SandboxRestored{SandboxRestored: &adlcv1.SandboxRestored{SuspendedSeq: seq}}}
}

func TestPendingSnapshot(t *testing.T) {
	tests := []struct {
		name    string
		events  []*adlcv1.Event // after run_started (seq 1)
		wantOK  bool
		wantSeq uint64
		wantRef string
	}{
		{name: "no sandbox events"},
		{name: "one suspended", events: []*adlcv1.Event{suspendedEvent("snapshots/r1/2.tar")}, wantOK: true, wantSeq: 2, wantRef: "snapshots/r1/2.tar"},
		{name: "suspended then restored", events: []*adlcv1.Event{suspendedEvent("a.tar"), restoredEvent(2)}},
		{name: "restored names another seq", events: []*adlcv1.Event{suspendedEvent("a.tar"), restoredEvent(9)}, wantOK: true, wantSeq: 2, wantRef: "a.tar"},
		{
			name:   "suspended, restored, suspended",
			events: []*adlcv1.Event{suspendedEvent("a.tar"), restoredEvent(2), suspendedEvent("b.tar")},
			wantOK: true, wantSeq: 4, wantRef: "b.tar",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store, err := eventlog.Open(ctx, filepath.Join(dir, "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			start := &adlcv1.Event{RunId: "r1", Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{}}}
			for _, ev := range append([]*adlcv1.Event{start}, tt.events...) {
				if _, err := store.Append(ctx, ev); err != nil {
					t.Fatal(err)
				}
			}
			snap, seq, ok, err := pendingSnapshot(ctx, store, "r1", dir)
			if err != nil || ok != tt.wantOK || seq != tt.wantSeq {
				t.Fatalf("seq %d, ok %v, err %v; want %d, %v", seq, ok, err, tt.wantSeq, tt.wantOK)
			}
			if !ok {
				return
			}
			want := sandbox.Snapshot{
				TarPath: filepath.Join(dir, tt.wantRef), SHA256: "sha256:" + tt.wantRef, Bytes: 3, ImageID: "img",
				ProcessesSkipped: sandbox.ProcessesNotSupported,
			}
			if snap != want {
				t.Fatalf("snap = %+v, want %+v", snap, want)
			}
		})
	}
}
