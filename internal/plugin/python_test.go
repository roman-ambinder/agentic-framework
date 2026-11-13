//go:build plugin_python

package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// repoRoot walks up from the working directory to the directory with go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}

// pythonHook returns a Config that runs the example plugin, or skips the
// test if `make python-hook-setup` has not been run.
func pythonHook(t *testing.T, env ...string) Config {
	t.Helper()
	root := repoRoot(t)
	dir := filepath.Join(root, "examples", "plugins", "python-hook")
	py := filepath.Join(dir, ".venv", "bin", "python")
	if _, err := os.Stat(py); err != nil {
		t.Skip("python hook not set up; run `make python-hook-setup`")
	}
	if _, err := os.Stat(filepath.Join(dir, "gen", "adlc", "v1", "plugin_pb2.py")); err != nil {
		t.Skip("python stubs missing; run `make python-hook-setup`")
	}
	return Config{Name: "python-hook", Command: []string{py, filepath.Join(dir, "hook_plugin.py")}, Env: env}
}

const denyWrites = "ADLC_HOOK_DENY_PATTERN=fs__write_.*"

func TestPythonHookDeniesMatchingTool(t *testing.T) {
	p := startHelper(t, pythonHook(t, denyWrites))
	call := model.ToolCall{ID: "c1", Name: "fs__write_file", Arguments: []byte(`{}`)}
	got, err := p.PreTool(context.Background(), call, testTool)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deny || !strings.Contains(got.Reason, "matches deny pattern") {
		t.Errorf("decision = %+v, want deny with reason", got)
	}
}

func TestPythonHookAllowsOtherTools(t *testing.T) {
	p := startHelper(t, pythonHook(t, denyWrites))
	got, err := p.PreTool(context.Background(), testCall, testTool)
	if err != nil {
		t.Fatal(err)
	}
	if got.Deny {
		t.Errorf("decision = %+v, want allow", got)
	}
	// fullmatch: a prefix must not make the pattern match.
	got, err = p.PreTool(context.Background(), model.ToolCall{ID: "c2", Name: "xfs__write_file"}, testTool)
	if err != nil || got.Deny {
		t.Errorf("xfs__write_file: %+v, %v; want allow", got, err)
	}
}

func TestPythonHookPatternMustMatchWholeName(t *testing.T) {
	p := startHelper(t, pythonHook(t, "ADLC_HOOK_DENY_PATTERN=fs__write"))
	for name, deny := range map[string]bool{"fs__write": true, "fs__write_file": false} {
		got, err := p.PreTool(context.Background(), model.ToolCall{ID: "c", Name: name}, testTool)
		if err != nil || got.Deny != deny {
			t.Errorf("%s: %+v, %v; want deny=%v", name, got, err, deny)
		}
	}
}

func TestPythonHookPostToolLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.jsonl")
	p := startHelper(t, pythonHook(t, "ADLC_HOOK_LOG="+logPath))
	res := toolrouter.Result{IsError: true, ExitCode: 3}
	if err := p.PostTool(context.Background(), testCall, res); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("log has %d lines: %q", len(lines), data)
	}
	var got struct {
		Tool     string `json:"tool"`
		IsError  bool   `json:"is_error"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Tool != "fs__read_file" || !got.IsError || got.ExitCode != 3 {
		t.Errorf("log line = %+v", got)
	}
}

func TestPythonHookStopsHarnessRun(t *testing.T) {
	ctx := context.Background()
	proc := startHelper(t, pythonHook(t, denyWrites))

	called := false
	router, err := toolrouter.NewStatic(toolrouter.StaticTool{
		Tool: toolrouter.Tool{
			Spec:   model.ToolSpec{Name: "fs__write_file", Description: "write a file"},
			Server: "fs",
			Risk:   adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL,
		},
		Fn: func(context.Context, json.RawMessage) (toolrouter.Result, error) {
			called = true
			t.Error("tool function was called")
			return toolrouter.Result{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := eventlog.Open(ctx, filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	call := model.ToolCall{ID: "t1", Name: "fs__write_file", Arguments: json.RawMessage(`{"path":"x","content":"y"}`)}
	fake := model.NewFake(
		model.FakeTurn{Response: &model.Response{
			Message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.ToolCallPart(call)}},
			Finish:  adlcv1.FinishReason_FINISH_REASON_TOOL_USE,
		}},
		model.FakeTurn{Response: &model.Response{
			Message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.TextPart("done")}},
			Finish:  adlcv1.FinishReason_FINISH_REASON_END_TURN,
		}},
	)
	cfg := harness.Config{
		RunID: "r1", AgentID: "test@1", AgentVersion: "test@1", Task: "do it", Model: "m",
		Hooks: []harness.Hook{proc},
	}
	state, err := harness.Run(ctx, cfg, harness.Deps{Log: store, Model: fake, Tools: router, Policy: policy.AllowAll{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if state.StopReason != adlcv1.StopReason_STOP_REASON_DENIED {
		t.Fatalf("stop = %s %q, want DENIED", state.StopReason, state.StopDetail)
	}
	events, err := store.Read(ctx, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		pd := ev.GetPolicyDecided()
		if pd == nil {
			continue
		}
		found = true
		if pd.GetSource() != adlcv1.DecisionSource_DECISION_SOURCE_HOOK ||
			pd.GetDecision() != adlcv1.Decision_DECISION_DENY || pd.GetRuleId() != "hook:python-hook" {
			t.Errorf("policy_decided = %v", pd)
		}
	}
	if !found {
		t.Error("no policy_decided event in the log")
	}
	if called {
		t.Error("denied tool ran")
	}
}

func TestPythonHookBadPattern(t *testing.T) {
	_, err := Start(context.Background(), pythonHook(t, "ADLC_HOOK_DENY_PATTERN=("))
	if !errors.Is(err, ErrExited) {
		t.Fatalf("err = %v, want ErrExited", err)
	}
	if !strings.Contains(err.Error(), "invalid ADLC_HOOK_DENY_PATTERN") {
		t.Errorf("err = %v, want invalid ADLC_HOOK_DENY_PATTERN", err)
	}
}
