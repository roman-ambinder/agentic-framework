package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/evals"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// writeAgentReport writes a report for agent with the given results (solved, usdMicros) pairs.
func writeAgentReport(t *testing.T, agent string, results ...evals.Result) string {
	t.Helper()
	a, err := agentconfig.Load(agent)
	if err != nil {
		t.Fatal(err)
	}
	for i := range results {
		results[i].AgentDigest = a.Digest()
		results[i].Provider = "fake"
		results[i].Difficulty = evals.Easy
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := evals.WriteReport(path, evals.Report{Suite: "s", Results: results}); err != nil {
		t.Fatal(err)
	}
	return path
}

func solvedResult(usd int64) evals.Result {
	return evals.Result{Grade: &evals.Grade{Solved: true, USDMicros: usd}}
}

func unsolvedResult() evals.Result { return evals.Result{Grade: &evals.Grade{}} }

// registerHello registers the agent file and returns the control db path.
func registerHello(t *testing.T, agent string) string {
	t.Helper()
	cdb := filepath.Join(t.TempDir(), "control.db")
	if code, _, e := runCLI("agent", "register", agent, "--control-db", cdb); code != 0 {
		t.Fatalf("register: %d %s", code, e)
	}
	return cdb
}

func TestAgentRegisterCommand(t *testing.T) {
	agent, _, _ := helloDir(t, "")
	cdb := filepath.Join(t.TempDir(), "control.db")
	code, out, e := runCLI("agent", "register", agent, "--control-db", cdb)
	if code != 0 || !strings.HasPrefix(out, "registered hello@1 (sha256:") {
		t.Fatalf("first: %d %q %q", code, out, e)
	}
	code, out, e = runCLI("agent", "register", agent, "--control-db", cdb)
	if code != 0 || !strings.HasPrefix(out, "already registered as hello@1 (") {
		t.Fatalf("second: %d %q %q", code, out, e)
	}
}

func TestAgentRegisterChangedContent(t *testing.T) {
	agent, _, _ := helloDir(t, "")
	cdb := registerHello(t, agent)
	yml, _ := os.ReadFile(agent)
	if err := os.WriteFile(agent, append(yml, "# changed\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, e := runCLI("agent", "register", agent, "--control-db", cdb)
	if code != 0 || !strings.HasPrefix(out, "registered hello@2 (") {
		t.Fatalf("%d %q %q", code, out, e)
	}
}

func TestAgentListCommand(t *testing.T) {
	agent, _, _ := helloDir(t, "")
	cdb := registerHello(t, agent)
	code, out, e := runCLI("agent", "list", "--control-db", cdb)
	if code != 0 {
		t.Fatalf("%d %s", code, e)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q", out)
	}
	if h := strings.Fields(lines[0]); strings.Join(h, " ") != "VERSION AGENT_ID DIGEST ENVS REGISTERED" {
		t.Errorf("header = %v", h)
	}
	row := strings.Fields(lines[1])
	if len(row) != 5 || row[0] != "hello@1" || row[1] != "hello@1" || len(row[2]) != 19 || !strings.HasPrefix(row[2], "sha256:") || row[3] != "-" {
		t.Errorf("row = %v", row)
	}
}

func TestAgentListMissingDB(t *testing.T) {
	cdb := filepath.Join(t.TempDir(), "control.db")
	code, out, _ := runCLI("agent", "list", "--control-db", cdb)
	if code != 0 || out != "no registered agents\n" {
		t.Fatalf("%d %q", code, out)
	}
	if _, err := os.Stat(cdb); !os.IsNotExist(err) {
		t.Errorf("control db was created: %v", err)
	}
}

func TestAgentShowCommand(t *testing.T) {
	agent, _, _ := helloDir(t, "")
	cdb := registerHello(t, agent)
	code, out, e := runCLI("agent", "show", "hello@1", "--control-db", cdb)
	if code != 0 || !strings.Contains(out, "version:    hello@1\n") || !strings.Contains(out, "file:       agent.yaml (") {
		t.Fatalf("%d %q %q", code, out, e)
	}
	code, _, e = runCLI("agent", "show", "hello@9", "--control-db", cdb)
	if code != 1 || !strings.Contains(e, "no such agent version: hello@9") {
		t.Fatalf("%d %q", code, e)
	}
}

func TestAgentPromoteCommand(t *testing.T) {
	agent, _, _ := helloDir(t, "")
	cdb := registerHello(t, agent)
	rep := writeAgentReport(t, agent, solvedResult(10000))
	code, out, e := runCLI("agent", "promote", "hello@1", "--to", "staging", "--report", rep, "--control-db", cdb)
	want := "promoted hello@1 to staging (pass@1 1.000 over 1 runs, median $0.010000)\n"
	if code != 0 || out != want {
		t.Fatalf("%d %q %q", code, out, e)
	}
	if _, out, _ := runCLI("agent", "show", "hello@1", "--control-db", cdb); !strings.Contains(out, "envs:       staging") {
		t.Errorf("show = %q", out)
	}
}

func TestAgentPromoteRefused(t *testing.T) {
	agent, _, _ := helloDir(t, "")
	cdb := registerHello(t, agent)
	rep := writeAgentReport(t, agent, solvedResult(0), unsolvedResult())
	code, _, e := runCLI("agent", "promote", "hello@1", "--to", "staging", "--report", rep, "--min-pass-at-1", "0.8", "--control-db", cdb)
	if code != 1 || !strings.Contains(e, "promotion refused: hello@1: pass@1 0.500 over 2 runs is below the minimum 0.800") {
		t.Fatalf("%d %q", code, e)
	}
	if _, out, _ := runCLI("agent", "show", "hello@1", "--control-db", cdb); !strings.Contains(out, "envs:       -") {
		t.Errorf("show = %q", out)
	}
}

func TestAgentPromoteNeedsNumberedVersion(t *testing.T) {
	agent, _, _ := helloDir(t, "")
	cdb := registerHello(t, agent)
	rep := writeAgentReport(t, agent, solvedResult(0))
	code, _, e := runCLI("agent", "promote", "hello@prod", "--to", "prod", "--report", rep, "--control-db", cdb)
	if code != 1 || !strings.Contains(e, `agent promote: want NAME@N, got "hello@prod"`) {
		t.Fatalf("%d %q", code, e)
	}
}

const helloAnswer = "result:  The file says: hello, world"

func TestRunRegisteredVersion(t *testing.T) {
	agent, script, db := helloDir(t, "")
	cdb := registerHello(t, agent)
	if err := os.WriteFile(agent, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, e := runCLI("run", "hello@1", "--task", "What does greeting.txt say?", "--fake-script", script, "--db", db, "--control-db", cdb)
	if code != 0 || !strings.Contains(out, helloAnswer) {
		t.Fatalf("%d %q %q", code, out, e)
	}
}

func TestRunPromotedEnv(t *testing.T) {
	agent, script, db := helloDir(t, "")
	cdb := registerHello(t, agent)
	rep := writeAgentReport(t, agent, solvedResult(0))
	if code, _, e := runCLI("agent", "promote", "hello@1", "--to", "staging", "--report", rep, "--control-db", cdb); code != 0 {
		t.Fatalf("promote: %d %s", code, e)
	}
	code, out, e := runCLI("run", "hello@staging", "--task", "t", "--fake-script", script, "--db", db, "--control-db", cdb)
	if code != 0 {
		t.Fatalf("%d %q %q", code, out, e)
	}
}

func TestRunRefWithoutControlDB(t *testing.T) {
	_, script, db := helloDir(t, "")
	cdb := filepath.Join(t.TempDir(), "none.db")
	code, _, e := runCLI("run", "hello@1", "--task", "t", "--fake-script", script, "--db", db, "--control-db", cdb)
	if code != 1 || !strings.Contains(e, "control db "+cdb+" does not exist") {
		t.Fatalf("%d %q", code, e)
	}
}

func TestRunAgentDirOnlyForRefs(t *testing.T) {
	agent, script, db := helloDir(t, "")
	code, _, e := runCLI("run", agent, "--task", "t", "--fake-script", script, "--db", db, "--agent-dir", "x")
	if code != 1 || !strings.Contains(e, "--agent-dir only applies") {
		t.Fatalf("%d %q", code, e)
	}
}

func TestRunAgentDirOverridesSourceDir(t *testing.T) {
	agent, script, db := helloDir(t, "")
	cdb := registerHello(t, agent)
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "workspace", "greeting.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, e := runCLI("run", "hello@1", "--task", "t", "--fake-script", script, "--db", db, "--control-db", cdb,
		"--agent-dir", other, "--run-id", "r1")
	if code != 0 {
		t.Fatalf("%d %q %q", code, out, e)
	}
	if got := toolOutputs(t, db, "r1"); !strings.Contains(got, "other") {
		t.Errorf("tool output = %q, want it to contain other", got)
	}
}

// toolOutputs returns the concatenated tool_call_completed outputs of a run.
func toolOutputs(t *testing.T, db, runID string) string {
	t.Helper()
	store, err := eventlog.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	events, err := store.Read(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, ev := range events {
		if c := ev.GetToolCallCompleted(); c != nil {
			sb.Write(c.GetOutput())
		}
	}
	return sb.String()
}

func TestResumeRegisteredVersion(t *testing.T) {
	agent, script, db := helloDir(t, "")
	crashedRun(t, agent, script, db)
	cdb := registerHello(t, agent)
	code, out, e := runCLI("resume", "r1", "hello@1", "--db", db, "--fake-script", script, "--control-db", cdb)
	if code != 0 || !strings.Contains(out, "stop:    completed") {
		t.Fatalf("%d %q %q", code, out, e)
	}
}

func TestApproveRegisteredVersion(t *testing.T) {
	e := pausedRun(t)
	cdb := registerHello(t, e.agent)
	if err := os.WriteFile(e.agent, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI("approve", "a-call_1", "r1", "hello@1", "--fake-script", e.approveScript, "--db", e.db, "--control-db", cdb)
	if code != 0 || !strings.Contains(out, "stop:    completed") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	if b, err := os.ReadFile(e.workspaceFile()); err != nil || string(b) != "hi" {
		t.Fatalf("out.txt = %q, %v", b, err)
	}
}
