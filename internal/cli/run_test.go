package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

func TestResolveCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in := []string{"adlc", "tool", "fs"}
	got, err := resolveCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != exe || got[1] != "tool" || got[2] != "fs" {
		t.Errorf("got %v", got)
	}
	if in[0] != "adlc" {
		t.Errorf("input modified: %v", in)
	}
	other, err := resolveCommand([]string{"python3", "x.py"})
	if err != nil {
		t.Fatal(err)
	}
	if other[0] != "python3" || other[1] != "x.py" {
		t.Errorf("got %v", other)
	}
}

func agentWithProvider(t *testing.T, provider string) *agentconfig.Agent {
	t.Helper()
	a, err := agentconfig.Parse([]byte("name: x\nversion: 1\nmodel:\n  provider: " + provider + "\n  name: m\npolicy:\n  default: deny\n"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBuildModelFakeNeedsScript(t *testing.T) {
	_, err := buildModel(agentWithProvider(t, "fake"), "")
	if err == nil || err.Error() != "run: provider fake needs --fake-script" {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildModelScriptOnlyForFake(t *testing.T) {
	_, err := buildModel(agentWithProvider(t, "anthropic"), "x.json")
	if err == nil || err.Error() != "run: --fake-script only works with provider fake" {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildModelAnthropic(t *testing.T) {
	m, err := buildModel(agentWithProvider(t, "anthropic"), "")
	if err != nil || m.Provider() != "anthropic" {
		t.Fatalf("m = %v, err = %v", m, err)
	}
}

func TestBuildModelOpenAI(t *testing.T) {
	m, err := buildModel(agentWithProvider(t, "openai"), "")
	if err != nil || m.Provider() != "openai" {
		t.Fatalf("m = %v, err = %v", m, err)
	}
	_, err = buildModel(agentWithProvider(t, "openai"), "x.json")
	if err == nil || err.Error() != "run: --fake-script only works with provider fake" {
		t.Fatalf("err = %v", err)
	}
}

const helloYAML = `name: hello
version: 1
model:
  provider: fake
system: |
  You answer questions about the files in the workspace. Use the fs tools.
tools:
  - server: fs
    command: ["adlc", "tool", "fs", "--root", "./workspace"]
    env:
      ADLC_RUN_AS_CLI: "1"
    risk:
      read_file: read
      list_dir: read
      write_file: write_local
budget:
  max_steps: 5
  max_tool_calls: 5
  max_wall_clock: 1m
policy:
  default: deny
  rules:
    - id: reads
      risk: read
      decision: allow
`

const maxTokensScript = `{"turns": [{"text": "cut", "finish": "max_tokens", "usage": {"input_tokens": 1, "output_tokens": 1}}]}`

// helloDir writes the example agent, workspace and script into a temp dir.
func helloDir(t *testing.T, script string) (agent, scriptPath, db string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("workspace/greeting.txt", "hello, world\n")
	agent = write("hello.yaml", helloYAML)
	if script == "" {
		b, err := os.ReadFile("../../examples/hello.fake.json")
		if err != nil {
			t.Fatal(err)
		}
		script = string(b)
	}
	scriptPath = write("script.json", script)
	return agent, scriptPath, filepath.Join(dir, "events.db")
}

func TestRunHelloWithFakeModel(t *testing.T) {
	agent, script, db := helloDir(t, "")
	var out, errOut bytes.Buffer
	code := Execute([]string{"run", agent, "--task", "What does greeting.txt say?", "--db", db, "--fake-script", script}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "stop:    completed\n") || !strings.Contains(out.String(), "result:  The file says: hello, world\n") {
		t.Fatalf("stdout = %q", out.String())
	}
	runID := strings.TrimPrefix(strings.SplitN(out.String(), "\n", 2)[0], "run:     ")
	ctx := context.Background()
	store, err := eventlog.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	st, err := store.State(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if st.ToolCalls != 1 || st.Status != eventlog.StatusFinished {
		t.Errorf("state: tool calls %d, status %v", st.ToolCalls, st.Status)
	}
	events, err := store.Read(ctx, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if c := ev.GetToolCallCompleted(); c != nil {
			found = true
			if !bytes.Contains(c.GetOutput(), []byte("hello, world")) {
				t.Errorf("tool output = %q", c.GetOutput())
			}
		}
	}
	if !found {
		t.Error("no tool_call_completed event")
	}
}

func TestRunNotCompletedExitsTwo(t *testing.T) {
	agent, script, db := helloDir(t, maxTokensScript)
	var out, errOut bytes.Buffer
	code := Execute([]string{"run", agent, "--task", "t", "--db", db, "--fake-script", script}, &out, &errOut)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	for _, want := range []string{"stop:    error\n", "detail:  output limit reached\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout %q lacks %q", out.String(), want)
		}
	}
	if strings.Contains(errOut.String(), "Error:") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestRunInvalidConfig(t *testing.T) {
	agent, script, db := helloDir(t, "")
	if err := os.WriteFile(agent, []byte(strings.Replace(helloYAML, "name: hello", "name: Bad", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Execute([]string{"run", agent, "--task", "t", "--db", db, "--fake-script", script}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "name: must match") {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
}

func TestRunRequiresTask(t *testing.T) {
	agent, script, db := helloDir(t, "")
	var out, errOut bytes.Buffer
	code := Execute([]string{"run", agent, "--db", db, "--fake-script", script}, &out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
}

func TestRunBadRunID(t *testing.T) {
	agent, script, db := helloDir(t, "")
	var out, errOut bytes.Buffer
	code := Execute([]string{"run", agent, "--task", "t", "--db", db, "--fake-script", script, "--run-id", "a b"}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "invalid --run-id") {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
}

func TestRunMissingFakeScriptExitsOne(t *testing.T) {
	agent, _, db := helloDir(t, "")
	var out, errOut bytes.Buffer
	code := Execute([]string{"run", agent, "--task", "t", "--db", db}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "needs --fake-script") {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
}

func TestRunCreatesDBDir(t *testing.T) {
	agent, script, db := helloDir(t, "")
	db = filepath.Join(filepath.Dir(db), "sub", "dir", "events.db")
	var out, errOut bytes.Buffer
	code := Execute([]string{"run", agent, "--task", "t", "--db", db, "--fake-script", script}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut.String())
	}
	if _, err := os.Stat(db); err != nil {
		t.Error(err)
	}
}

func TestExampleFilesLoad(t *testing.T) {
	if _, err := agentconfig.Load("../../examples/hello.yaml"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADLC_ANTHROPIC_MODEL", "test-model")
	a, err := agentconfig.Load("../../examples/hello-anthropic.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if a.Model.Provider != "anthropic" {
		t.Errorf("provider = %q", a.Model.Provider)
	}
	turns, err := model.LoadFakeScript("../../examples/hello.fake.json")
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns = %d, err = %v", len(turns), err)
	}
}

func TestModelNameFlagReplacesUnsetVariable(t *testing.T) {
	t.Setenv("ADLC_ANTHROPIC_MODEL", "")
	a, err := loadAgent(runOptions{agentPath: "../../examples/hello-anthropic.yaml", modelName: "cli-model"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Model.Name != "cli-model" {
		t.Errorf("name = %q", a.Model.Name)
	}
	if _, err := loadAgent(runOptions{agentPath: "../../examples/hello-anthropic.yaml"}); err == nil {
		t.Error("want error when variable unset and no --model-name")
	}
}
