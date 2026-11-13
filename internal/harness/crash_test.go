//go:build unix

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// fixedScript is two tool steps and a final answer (16 events), the same in
// the child process and in the test.
func fixedScript() []model.FakeTurn {
	echo := func(id string) model.ToolCall {
		return model.ToolCall{ID: id, Name: "t__echo", Arguments: json.RawMessage(`{"say":"` + id + `"}`)}
	}
	return []model.FakeTurn{toolTurn(echo("t1")), toolTurn(echo("t2")), textTurn("final answer")}
}

func fixedDeps(t *testing.T) Deps {
	t.Helper()
	echo := toolrouter.StaticTool{
		Tool: toolrouter.Tool{
			Spec: model.ToolSpec{Name: "t__echo", Description: "echo"}, Server: "t", Risk: adlcv1.RiskClass_RISK_CLASS_READ,
		},
		Fn: func(_ context.Context, args json.RawMessage) (toolrouter.Result, error) {
			return toolrouter.Result{Output: args}, nil
		},
	}
	tools, err := toolrouter.NewStatic(echo)
	if err != nil {
		t.Fatal(err)
	}
	return Deps{Tools: tools, Policy: policy.AllowAll{}}
}

// killingLog kills the process right after the n-th successful Append, so the
// log ends exactly where a crash would leave it.
type killingLog struct {
	Log
	mu    sync.Mutex
	after int
	done  int
}

func (k *killingLog) Append(ctx context.Context, ev *adlcv1.Event) (*adlcv1.Event, error) {
	stored, err := k.Log.Append(ctx, ev)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.done++
	kill := k.done == k.after
	k.mu.Unlock()
	if kill {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		select {} // wait for the signal
	}
	return stored, nil
}

// TestHelperCrashingRun is not a real test: TestResumeAfterCrash runs the
// test binary again with ADLC_CRASH_HELPER=1 to execute it in a child process.
func TestHelperCrashingRun(t *testing.T) {
	if os.Getenv("ADLC_CRASH_HELPER") != "1" {
		t.Skip("helper process only")
	}
	after, err := strconv.Atoi(os.Getenv("ADLC_CRASH_AFTER"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := eventlog.Open(context.Background(), os.Getenv("ADLC_CRASH_DB"))
	if err != nil {
		t.Fatal(err)
	}
	deps := fixedDeps(t)
	deps.Log, deps.Model = &killingLog{Log: store, after: after}, model.NewFake(fixedScript()...)
	_, _ = Run(context.Background(), baseConfig(), deps)
	t.Fatal("the run ended without being killed")
}

func TestResumeAfterCrash(t *testing.T) {
	full := newLog(t)
	deps := fixedDeps(t)
	deps.Log, deps.Model = full, model.NewFake(fixedScript()...)
	if _, err := Run(context.Background(), baseConfig(), deps); err != nil {
		t.Fatal(err)
	}
	all, err := full.Read(context.Background(), "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	total := len(all)
	if total != 16 {
		t.Fatalf("the script has %d events, want 16", total)
	}
	for n := 1; n < total; n++ {
		t.Run(fmt.Sprintf("after_%d", n), func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "events.db")
			crashChild(t, db, n)
			store, err := eventlog.Open(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			evs, err := store.Read(context.Background(), "r1", 0)
			if err != nil || len(evs) != n {
				t.Fatalf("log has %d events (err %v) after the kill, want %d", len(evs), err, n)
			}
			done := len(pick(evs, (*adlcv1.Event).GetModelCallCompleted))
			r := resumeOK(t, store, baseConfig(), fixedDeps(t), fixedScript()[done:]...)
			wantStop(t, r.state, stopCompleted, "")
			if r.state.Result != "final answer" {
				t.Fatalf("result = %q", r.state.Result)
			}
			assertEveryCallHasResult(t, r.events)
		})
	}
}

// crashChild runs the helper test in a child process and checks the kill.
func crashChild(t *testing.T, db string, after int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperCrashingRun$")
	cmd.Env = append(os.Environ(), "ADLC_CRASH_HELPER=1", "ADLC_CRASH_DB="+db, "ADLC_CRASH_AFTER="+strconv.Itoa(after))
	err := cmd.Run()
	if err == nil {
		t.Fatal("the child process exited normally")
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		t.Fatalf("child was not killed by a signal: %v", err)
	}
}

// assertEveryCallHasResult checks that each tool_call_requested has a
// tool_call_completed, which providers require before the next model call.
func assertEveryCallHasResult(t *testing.T, events []*adlcv1.Event) {
	t.Helper()
	requested := map[string]bool{}
	for _, ev := range events {
		if c := ev.GetToolCallRequested(); c != nil {
			requested[c.GetCall().GetId()] = true
		}
		if c := ev.GetToolCallCompleted(); c != nil {
			delete(requested, c.GetCallId())
		}
	}
	if len(requested) != 0 {
		t.Fatalf("tool calls without a result: %v", requested)
	}
}
