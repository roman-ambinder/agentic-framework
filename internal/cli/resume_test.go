package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

// stopAfterLog stops writing after n events, so the log ends where a crash
// would leave it.
type stopAfterLog struct {
	harness.Log
	mu sync.Mutex
	n  int
}

var errStopped = errors.New("log stopped")

func (l *stopAfterLog) Append(ctx context.Context, ev *adlcv1.Event) (*adlcv1.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n == 0 {
		return nil, errStopped
	}
	l.n--
	return l.Log.Append(ctx, ev)
}

// crashedRun starts run "r1" of the hello agent and stops its log after two events
// (run_started, model_call_started).
func crashedRun(t *testing.T, agent, script, db string) {
	t.Helper()
	ctx := context.Background()
	o := runOptions{
		agentPath: agent, task: "What does greeting.txt say?", db: db, fakeScript: script,
		startHook: (&hookHarness{}).start,
	} // agents with hooks must not start real plugins
	a, err := loadAgent(o)
	if err != nil {
		t.Fatal(err)
	}
	store, err := eventlog.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg, deps, cleanup, err := buildRun(ctx, a, o, "r1", &stopAfterLog{Log: store, n: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cleanup() }()
	if _, err := harness.Run(ctx, cfg, deps); !errors.Is(err, errStopped) {
		t.Fatalf("Run err = %v, want the log to stop", err)
	}
}

func TestResumeCommandFinishedRun(t *testing.T) {
	agent, script, db := helloDir(t, "")
	var out, errOut bytes.Buffer
	if code := Execute([]string{"run", agent, "--task", "t", "--db", db, "--fake-script", script, "--run-id", "r1"}, &out, &errOut); code != 0 {
		t.Fatalf("setup: code = %d, stderr = %s", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	code := Execute([]string{"resume", "r1", "--agent", agent, "--db", db, "--fake-script", script}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "run already finished") {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestResumeCommandCompletesRun(t *testing.T) {
	agent, script, db := helloDir(t, "")
	crashedRun(t, agent, script, db)
	var out, errOut bytes.Buffer
	code := Execute([]string{"resume", "r1", "--agent", agent, "--db", db, "--fake-script", script}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	for _, want := range []string{"run:     r1\n", "stop:    completed\n", "result:  The file says: hello, world\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout %q lacks %q", out.String(), want)
		}
	}
	store, err := eventlog.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Read(context.Background(), "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(pick(events, (*adlcv1.Event).GetRunResumed)); n != 1 {
		t.Fatalf("%d run_resumed events, want 1", n)
	}
}

func pick[T any](evs []*adlcv1.Event, get func(*adlcv1.Event) *T) []*T {
	var out []*T
	for _, ev := range evs {
		if v := get(ev); v != nil {
			out = append(out, v)
		}
	}
	return out
}

func TestResumeCommandNeedsAgent(t *testing.T) {
	code, _, stderr := run(t, "resume", "r1")
	if code != 1 || !strings.Contains(stderr, "resume: an agent file is required (AGENT_YAML or --agent)") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestResumeAgentPath(t *testing.T) {
	tests := []struct {
		name       string
		positional []string
		flag       string
		want       string
		wantErr    string
	}{
		{name: "neither", wantErr: "resume: an agent file is required (AGENT_YAML or --agent)"},
		{name: "positional only", positional: []string{"a.yaml"}, want: "a.yaml"},
		{name: "flag only", flag: "a.yaml", want: "a.yaml"},
		{name: "both same", positional: []string{"a.yaml"}, flag: "a.yaml", want: "a.yaml"},
		{name: "both same after clean", positional: []string{"./a.yaml"}, flag: "a.yaml", want: "./a.yaml"},
		{name: "both different", positional: []string{"a.yaml"}, flag: "b.yaml", wantErr: "resume: AGENT_YAML and --agent disagree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resumeAgentPath(tt.positional, tt.flag)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestResumeCommandPositionalAgentCompletesRun(t *testing.T) {
	agent, script, db := helloDir(t, "")
	crashedRun(t, agent, script, db)
	code, out, stderr := run(t, "resume", "r1", agent, "--db", db, "--fake-script", script)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(out, "stop:    completed\n") {
		t.Errorf("stdout %q lacks completed stop", out)
	}
}

func TestResumeCommandAgentFlagStillWorks(t *testing.T) {
	agent, script, db := helloDir(t, "")
	crashedRun(t, agent, script, db)
	code, _, stderr := run(t, "resume", "r1", "--agent", agent, "--db", db, "--fake-script", script)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
}

func TestResumeCommandAgentsDisagree(t *testing.T) {
	agent, script, db := helloDir(t, "")
	code, _, stderr := run(t, "resume", "r1", agent, "--agent", agent+".other", "--db", db, "--fake-script", script)
	if code != 1 || !strings.Contains(stderr, "resume: AGENT_YAML and --agent disagree") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestResumeCommandUnknownRun(t *testing.T) {
	agent, script, db := helloDir(t, "")
	crashedRun(t, agent, script, db)
	code, _, stderr := run(t, "resume", "nope", "--agent", agent, "--db", db, "--fake-script", script)
	if code != 1 || !strings.Contains(stderr, "run not found") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

func TestResumeCommandMissingLog(t *testing.T) {
	agent, script, db := helloDir(t, "")
	code, _, stderr := run(t, "resume", "r1", "--agent", agent, "--db", db, "--fake-script", script)
	if code != 1 || !strings.Contains(stderr, "does not exist") {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
}

// fakeSandboxes answers FindByRunID and Exec from fields; the other
// backend methods come from the embedded Pausable.
type fakeSandboxes struct {
	sandboxtest.Pausable
	found    []sandbox.Handle
	probeErr error
	probed   []string
	closed   bool
}

func (f *fakeSandboxes) FindByRunID(context.Context, string) ([]sandbox.Handle, error) {
	return f.found, nil
}

func (f *fakeSandboxes) Exec(_ context.Context, h sandbox.Handle, cmd []string, _ time.Duration) (sandbox.ExecResult, error) {
	f.probed = append(f.probed, h.ID+" "+strings.Join(cmd, " "))
	return sandbox.ExecResult{}, f.probeErr
}

func (f *fakeSandboxes) Close() error { f.closed = true; return nil }

const sandboxAgentYAML = `name: coder
version: 1
model:
  provider: fake
  name: m
sandbox:
  workspace: .
tools:
  - server: shell
    command: ["adlc", "tool", "shell", "--sandbox", "{{sandbox_id}}"]
policy:
  default: deny
`

func sandboxAgent(t *testing.T, yaml string) *agentconfig.Agent {
	t.Helper()
	a, err := agentconfig.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// emptySource is a sandboxSource over b and an empty event log.
func emptySource(t *testing.T, b sandboxLookup) sandboxSource {
	t.Helper()
	dir := t.TempDir()
	store, err := eventlog.Open(context.Background(), filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return sandboxSource{open: func(context.Context) (sandboxLookup, error) { return b, nil }, store: store, dbDir: dir}
}

func TestAttachSandbox(t *testing.T) {
	h := sandbox.Handle{ID: "abc123"}
	tests := []struct {
		name    string
		fake    *fakeSandboxes
		wantErr string
	}{
		{"one live sandbox", &fakeSandboxes{found: []sandbox.Handle{h}}, ""},
		{"none", &fakeSandboxes{}, `sandbox for run "r1" is not running; its workspace changes cannot be recovered. Start a new run.`},
		{
			"probe fails", &fakeSandboxes{found: []sandbox.Handle{h}, probeErr: errors.New("container stopped")},
			`sandbox for run "r1" is not running; its workspace changes cannot be recovered. Start a new run.`,
		},
		{
			"two", &fakeSandboxes{found: []sandbox.Handle{h, {ID: "def456"}}},
			`found 2 sandboxes for run "r1"; remove the extra ones with docker rm`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := sandboxAgent(t, sandboxAgentYAML)
			sb, err := attachSandbox(context.Background(), a, "r1", emptySource(t, tt.fake), io.Discard)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr || sb != nil || !tt.fake.closed {
					t.Fatalf("err = %v, want %q; sb %v, closed %v", err, tt.wantErr, sb, tt.fake.closed)
				}
				if strings.Contains(strings.Join(a.Tools[0].Command, " "), "abc123") {
					t.Fatal("commands were expanded despite the error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(a.Tools[0].Command, " "); got != "adlc tool shell --sandbox abc123" {
				t.Fatalf("command = %q", got)
			}
			if len(tt.fake.probed) != 1 || tt.fake.probed[0] != "abc123 true" || tt.fake.closed {
				t.Fatalf("probed %v, closed %v (the caller closes)", tt.fake.probed, tt.fake.closed)
			}
			if sb == nil || sb.handle.ID != "abc123" {
				t.Fatalf("sb = %+v", sb)
			}
			sb.close()
			if !tt.fake.closed {
				t.Fatal("close did not close the backend")
			}
		})
	}
}

func TestAttachSandboxSpecWithoutPlaceholderFindsContainer(t *testing.T) {
	a := sandboxAgent(t, strings.Replace(sandboxAgentYAML, "{{sandbox_id}}", "fixed", 1))
	fake := &fakeSandboxes{found: []sandbox.Handle{{ID: "abc123"}}}
	sb, err := attachSandbox(context.Background(), a, "r1", emptySource(t, fake), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if sb == nil || sb.handle.ID != "abc123" {
		t.Fatalf("sb = %+v, want the kept container attached so finish can copy it out", sb)
	}
}

func TestAttachSandboxSpecWithoutPlaceholderNoContainer(t *testing.T) {
	a := sandboxAgent(t, strings.Replace(sandboxAgentYAML, "{{sandbox_id}}", "fixed", 1))
	fake := &fakeSandboxes{}
	sb, err := attachSandbox(context.Background(), a, "r1", emptySource(t, fake), io.Discard)
	if err != nil || sb != nil {
		t.Fatalf("sb = %v, err = %v; want nil, nil (resume without a sandbox, as before)", sb, err)
	}
	if !fake.closed {
		t.Error("backend not closed")
	}
}

func TestAttachSandboxSpecWithoutPlaceholderBackendUnavailable(t *testing.T) {
	a := sandboxAgent(t, strings.Replace(sandboxAgentYAML, "{{sandbox_id}}", "fixed", 1))
	src := emptySource(t, nil)
	src.open = func(context.Context) (sandboxLookup, error) { return nil, errors.New("docker unavailable") }
	var out strings.Builder
	sb, err := attachSandbox(context.Background(), a, "r1", src, &out)
	if err != nil || sb != nil {
		t.Fatalf("sb = %v, err = %v; want nil, nil (no Docker: continue without a sandbox, as before)", sb, err)
	}
	if !strings.Contains(out.String(), "sandbox: could not look for a kept container: docker unavailable") {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestAttachSandboxPlaceholderBackendUnavailableFails(t *testing.T) {
	a := sandboxAgent(t, sandboxAgentYAML)
	src := emptySource(t, nil)
	src.open = func(context.Context) (sandboxLookup, error) { return nil, errors.New("docker unavailable") }
	if _, err := attachSandbox(context.Background(), a, "r1", src, io.Discard); err == nil {
		t.Fatal("want an error: the tools need the sandbox")
	}
}
