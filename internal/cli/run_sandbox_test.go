package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

// recordingBackend is a runBackend over the host sandbox that records calls.
type recordingBackend struct {
	*sandboxtest.Host
	mu        sync.Mutex
	specs     []sandbox.Spec
	destroyed []string
	copied    int
	closed    bool
}

func (r *recordingBackend) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Handle, error) {
	r.mu.Lock()
	r.specs = append(r.specs, spec)
	r.mu.Unlock()
	return r.Host.Create(ctx, spec)
}

func (r *recordingBackend) CopyOut(ctx context.Context, h sandbox.Handle, p string) ([]byte, error) {
	r.mu.Lock()
	r.copied++
	r.mu.Unlock()
	return r.Host.CopyOut(ctx, h, p)
}

func (r *recordingBackend) Destroy(ctx context.Context, h sandbox.Handle) error {
	r.mu.Lock()
	r.destroyed = append(r.destroyed, h.ID)
	r.mu.Unlock()
	return r.Host.Destroy(ctx, h)
}

func (r *recordingBackend) FindByRunID(context.Context, string) ([]sandbox.Handle, error) {
	return nil, nil
}

func (r *recordingBackend) Close() error { r.closed = true; return nil }

// sandboxRun writes the hello agent with a sandbox section (and the given
// policy decision for reads) and runs it with backend. It returns stdout,
// the error and the workspace-out directory.
func sandboxRun(t *testing.T, decision string, flags []string, open openRunBackend) (string, string, error) {
	t.Helper()
	agent, script, db := helloDir(t, "")
	yml := helloYAML + "sandbox:\n  workspace: ./workspace\n  network: true\n  cpus: 2\n  memory_mb: 256\n"
	yml = strings.Replace(yml, "decision: allow", "decision: "+decision, 1)
	if err := os.WriteFile(agent, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	o := runOptions{agentPath: agent, task: "t", db: db, fakeScript: script, runID: "r1", workspaceOut: out}
	for _, f := range flags {
		if f == "--keep-sandbox" {
			o.keepSandbox = true
		}
	}
	var stdout bytes.Buffer
	err := runAgentWith(context.Background(), o, &stdout, open)
	return stdout.String(), out, err
}

func newRecordingBackend(t *testing.T) *recordingBackend {
	t.Helper()
	return &recordingBackend{Host: sandboxtest.NewHost(t.TempDir())}
}

func TestRunWithoutSandboxSectionNeverOpensBackend(t *testing.T) {
	agent, script, db := helloDir(t, "")
	o := runOptions{agentPath: agent, task: "t", db: db, fakeScript: script}
	err := runAgentWith(context.Background(), o, &bytes.Buffer{}, func(context.Context) (runBackend, error) {
		t.Fatal("backend must not be opened")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunSandboxDockerUnavailable(t *testing.T) {
	_, _, err := sandboxRun(t, "allow", nil, func(context.Context) (runBackend, error) { return nil, errors.New("no daemon") })
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 || err.Error() != "run: docker is not available: no daemon" {
		t.Fatalf("err = %v", err)
	}
}

func TestRunSandboxCreatesCopiesOutAndDestroys(t *testing.T) {
	b := newRecordingBackend(t)
	stdout, out, err := sandboxRun(t, "allow", nil, func(context.Context) (runBackend, error) { return b, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(b.specs) != 1 {
		t.Fatalf("%d creates", len(b.specs))
	}
	s := b.specs[0]
	if s.RunID != "r1" || s.Image != "golang:1.26" || !s.Network || s.CPUs != 2 || s.MemoryBytes != 256<<20 ||
		filepath.Base(s.WorkspaceDir) != "workspace" {
		t.Errorf("spec = %+v", s)
	}
	got, err := os.ReadFile(filepath.Join(out, "greeting.txt"))
	if err != nil || string(got) != "hello, world\n" {
		t.Errorf("copied file = %q, %v", got, err)
	}
	if !strings.Contains(stdout, "workspace: "+out+"\n") {
		t.Errorf("stdout = %q", stdout)
	}
	if len(b.destroyed) != 1 || !b.closed {
		t.Errorf("destroyed %v, closed %v", b.destroyed, b.closed)
	}
}

func TestRunSandboxKeepSandboxFlag(t *testing.T) {
	b := newRecordingBackend(t)
	stdout, out, err := sandboxRun(t, "allow", []string{"--keep-sandbox"}, func(context.Context) (runBackend, error) { return b, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(b.destroyed) != 0 {
		t.Errorf("destroyed %v, want none", b.destroyed)
	}
	if _, err := os.Stat(filepath.Join(out, "greeting.txt")); err != nil {
		t.Error(err)
	}
	if !strings.Contains(stdout, "sandbox: kept host-1\n") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestRunSandboxKeptWhenAwaitingApproval(t *testing.T) {
	b := newRecordingBackend(t)
	stdout, out, err := sandboxRun(t, "needs_approval", nil, func(context.Context) (runBackend, error) { return b, nil })
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("err = %v, want exit 2", err)
	}
	if !strings.Contains(stdout, "stop:    awaiting_approval\n") || !strings.Contains(stdout, "sandbox: kept host-1 for resume\n") {
		t.Errorf("stdout = %q", stdout)
	}
	if len(b.destroyed) != 0 || b.copied != 0 {
		t.Errorf("destroyed %v, copied %d; want neither", b.destroyed, b.copied)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("workspace was copied out")
	}
}

func TestRunSandboxDestroyedWhenBuildFails(t *testing.T) {
	b := newRecordingBackend(t)
	agent, _, db := helloDir(t, "")
	yml := helloYAML + "sandbox:\n  workspace: ./workspace\n"
	if err := os.WriteFile(agent, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	o := runOptions{agentPath: agent, task: "t", db: db} // provider fake without --fake-script
	err := runAgentWith(context.Background(), o, &bytes.Buffer{}, func(context.Context) (runBackend, error) { return b, nil })
	if err == nil || len(b.destroyed) != 1 {
		t.Fatalf("err = %v, destroyed %v", err, b.destroyed)
	}
}

func TestStartRunSandboxExpandsPlaceholder(t *testing.T) {
	b := newRecordingBackend(t)
	a := sandboxAgent(t, sandboxAgentYAML)
	sb, err := startRunSandbox(context.Background(), a, "r1", func(context.Context) (runBackend, error) { return b, nil })
	if err != nil || sb == nil {
		t.Fatalf("sb = %v, err = %v", sb, err)
	}
	if got := strings.Join(a.Tools[0].Command, " "); got != "adlc tool shell --sandbox host-1" {
		t.Errorf("command = %q", got)
	}
}
