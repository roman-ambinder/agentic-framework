package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/plugin"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// fakeHook is an in-process hook that records its lifecycle in rec.
type fakeHook struct {
	name   string
	deny   bool
	rec    *[]string
	closed int
}

func (h *fakeHook) Name() string { return h.name }

func (h *fakeHook) PreTool(context.Context, model.ToolCall, toolrouter.Tool) (harness.HookDecision, error) {
	*h.rec = append(*h.rec, "pre:"+h.name)
	return harness.HookDecision{Deny: h.deny, Reason: "no writes"}, nil
}

func (h *fakeHook) PostTool(context.Context, model.ToolCall, toolrouter.Result) error { return nil }

func (h *fakeHook) Close() error {
	h.closed++
	*h.rec = append(*h.rec, "close:"+h.name)
	return nil
}

// hookHarness records the configs it was asked to start and fails on failName.
type hookHarness struct {
	rec      []string
	cfgs     []plugin.Config
	hooks    []*fakeHook
	failName string
	deny     string
}

func (hh *hookHarness) start(_ context.Context, cfg plugin.Config) (hookProcess, error) {
	hh.cfgs = append(hh.cfgs, cfg)
	hh.rec = append(hh.rec, "start:"+cfg.Name)
	if cfg.Name == hh.failName {
		return nil, errors.New("plugin: start timeout")
	}
	h := &fakeHook{name: cfg.Name, deny: cfg.Name == hh.deny, rec: &hh.rec}
	hh.hooks = append(hh.hooks, h)
	return h, nil
}

// hookAgent writes the hello agent plus two hooks and returns its path.
func hookAgent(t *testing.T) (agent, script, db string) {
	t.Helper()
	agent, script, db = helloDir(t, "")
	b, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	extra := `hooks:
  - name: first
    command: ["python3", "first.py"]
    env: {B: "2", A: "1"}
    start_timeout: 3s
    call_timeout: 4s
  - name: second
    command: ["adlc", "hook"]
`
	if err := os.WriteFile(agent, append(b, extra...), 0o644); err != nil {
		t.Fatal(err)
	}
	return agent, script, db
}

func TestRunStartsHooksInOrderAndClosesThem(t *testing.T) {
	agent, script, db := hookAgent(t)
	hh := &hookHarness{}
	o := runOptions{agentPath: agent, task: "t", db: db, fakeScript: script, runID: "r1", startHook: hh.start}
	var out bytes.Buffer
	if err := runAgent(context.Background(), o, &out); err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []string{"start:first", "start:second", "pre:first", "pre:second", "close:second", "close:first"}
	if !reflect.DeepEqual(hh.rec, want) {
		t.Fatalf("rec = %v, want %v", hh.rec, want)
	}
	c := hh.cfgs[0]
	if !reflect.DeepEqual(c.Command, []string{"python3", "first.py"}) || !reflect.DeepEqual(c.Env, []string{"A=1", "B=2"}) ||
		c.StartTimeout != 3*time.Second || c.CallTimeout != 4*time.Second || c.Dir == "" {
		t.Errorf("config = %+v", c)
	}
	exe, _ := os.Executable()
	if got := hh.cfgs[1].Command[0]; got != exe {
		t.Errorf("adlc not resolved: %q", got)
	}
}

func TestRunHookStartFailureStopsBeforeRun(t *testing.T) {
	agent, script, db := hookAgent(t)
	hh := &hookHarness{failName: "second"}
	o := runOptions{agentPath: agent, task: "t", db: db, fakeScript: script, runID: "r1", startHook: hh.start}
	err := runAgent(context.Background(), o, &bytes.Buffer{})
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 || !strings.Contains(err.Error(), "run: hook second: plugin: start timeout") {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"start:first", "start:second", "close:first"}; !reflect.DeepEqual(hh.rec, want) {
		t.Fatalf("rec = %v, want %v", hh.rec, want)
	}
	if hh.hooks[0].closed != 1 {
		t.Errorf("first closed %d times", hh.hooks[0].closed)
	}
}

func TestRunDenyingHookEndsRunDenied(t *testing.T) {
	agent, script, db := hookAgent(t)
	hh := &hookHarness{deny: "first"}
	o := runOptions{agentPath: agent, task: "t", db: db, fakeScript: script, runID: "r1", startHook: hh.start}
	var out bytes.Buffer
	err := runAgent(context.Background(), o, &out)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "stop:    denied") {
		t.Fatalf("stdout = %q", out.String())
	}
	if want := []string{"start:first", "start:second", "pre:first", "close:second", "close:first"}; !reflect.DeepEqual(hh.rec, want) {
		t.Fatalf("rec = %v, want %v", hh.rec, want)
	}
}

func TestResumeStartsHooksAndClosesOnError(t *testing.T) {
	agent, script, db := hookAgent(t)
	crashedRun(t, agent, script, db)
	hh := &hookHarness{failName: "second"}
	o := runOptions{agentPath: agent, db: db, fakeScript: script, startHook: hh.start}
	var out bytes.Buffer
	err := resumeAgent(context.Background(), o, "r1", &out, nil)
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 1 || !strings.Contains(err.Error(), "run: hook second: ") {
		t.Fatalf("err = %v", err)
	}
	if hh.hooks[0].closed != 1 {
		t.Errorf("first closed %d times", hh.hooks[0].closed)
	}

	hh = &hookHarness{deny: "second"}
	o.startHook = hh.start
	err = resumeAgent(context.Background(), o, "r1", &out, nil)
	if !errors.As(err, &ee) || ee.code != 2 || !strings.Contains(out.String(), "stop:    denied") {
		t.Fatalf("err = %v, stdout = %q", err, out.String())
	}
	if hh.hooks[0].closed != 1 || hh.hooks[1].closed != 1 {
		t.Errorf("closed = %d, %d", hh.hooks[0].closed, hh.hooks[1].closed)
	}
}
