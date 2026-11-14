package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

const forkScript = `{"turns": [{"text": "Forked answer", "finish": "end_turn", "usage": {"input_tokens": 5, "output_tokens": 2}}]}`

// forkSetup runs the hello agent as run o1 and writes the fork agent
// (hello2, plus extra yaml) and its script next to it.
func forkSetup(t *testing.T, extra string) (agent2, script2, db string) {
	t.Helper()
	agent, script, db := helloDir(t, "")
	if code, _, errOut := execCLI(t, "run", agent, "--task", "What does greeting.txt say?", "--db", db, "--fake-script", script, "--run-id", "o1"); code != 0 {
		t.Fatalf("setup: code %d: %s", code, errOut)
	}
	dir := filepath.Dir(agent)
	agent2 = filepath.Join(dir, "hello2.yaml")
	if err := os.WriteFile(agent2, []byte(strings.Replace(helloYAML, "name: hello", "name: hello2", 1)+extra), 0o644); err != nil {
		t.Fatal(err)
	}
	script2 = filepath.Join(dir, "script2.json")
	if err := os.WriteFile(script2, []byte(forkScript), 0o644); err != nil {
		t.Fatal(err)
	}
	return agent2, script2, db
}

func storeRuns(t *testing.T, db string) []string {
	t.Helper()
	s, err := eventlog.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ids, err := s.Runs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestForkCommandContinuesFromStep(t *testing.T) {
	agent2, script2, db := forkSetup(t, "")
	code, out, errOut := execCLI(t, "fork", "o1", agent2, "--at-step", "1", "--run-id", "f1", "--db", db, "--fake-script", script2)
	if code != 0 {
		t.Fatalf("code %d, stderr %s, stdout %s", code, errOut, out)
	}
	for _, want := range []string{
		"forked:  o1 at step 1 (6 events copied)\n", "run:     f1\n", "agent:   hello2@1\n",
		"steps:   2  tool calls: 1  errors: 0\n", "result:  Forked answer\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	s, err := eventlog.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	evs, err := s.Read(context.Background(), "f1", 0)
	if err != nil {
		t.Fatal(err)
	}
	rs := evs[0].GetRunStarted()
	if rs.GetForkedFromRunId() != "o1" || rs.GetForkedAtStep() != 1 {
		t.Fatalf("run_started = %v", rs)
	}
}

func TestForkCommandErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown origin", []string{"fork", "nope", "AGENT", "--at-step", "1", "--run-id", "f1"}, "run not found"},
		{"no checkpoint", []string{"fork", "o1", "AGENT", "--at-step", "2", "--run-id", "f1"}, "no checkpoint for that step"},
		{"run exists", []string{"fork", "o1", "AGENT", "--at-step", "1", "--run-id", "o1"}, "run already exists"},
		{"step zero", []string{"fork", "o1", "AGENT", "--at-step", "0", "--run-id", "f1"}, "fork: --at-step must be at least 1"},
		{"no step", []string{"fork", "o1", "AGENT", "--run-id", "f1"}, `required flag(s) "at-step" not set`},
		{"bad run id", []string{"fork", "o1", "AGENT", "--at-step", "1", "--run-id", "bad id"}, "fork: invalid --run-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent2, script2, db := forkSetup(t, "")
			args := append([]string{}, tt.args...)
			for i, a := range args {
				if a == "AGENT" {
					args[i] = agent2
				}
			}
			args = append(args, "--db", db, "--fake-script", script2)
			code, out, errOut := execCLI(t, args...)
			if code != 1 || !strings.Contains(errOut, tt.want) {
				t.Fatalf("code %d, stderr %q, want %q", code, errOut, tt.want)
			}
			if out != "" {
				t.Errorf("stdout = %q", out)
			}
			if got := storeRuns(t, db); !reflect.DeepEqual(got, []string{"o1"}) {
				t.Errorf("runs = %v", got)
			}
		})
	}
}

const forkSandboxYAML = "sandbox:\n  workspace: ./workspace\n"

func TestForkSandboxIsFresh(t *testing.T) {
	agent2, script2, db := forkSetup(t, forkSandboxYAML)
	b := newRecordingBackend(t)
	var out bytes.Buffer
	o := forkOptions{runOptions: runOptions{
		agentPath: agent2, db: db, fakeScript: script2, runID: "f1", workspaceOut: filepath.Join(t.TempDir(), "out"),
	}, atStep: 1}
	err := forkAgentWith(context.Background(), o, "o1", &out, func(context.Context) (runBackend, error) { return b, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(b.specs) != 1 || b.specs[0].RunID != "f1" {
		t.Fatalf("specs = %+v", b.specs)
	}
	warn := "warning: the sandbox starts from the agent file's workspace; workspace changes of run o1 up to step 1 are not restored\n"
	if !strings.Contains(out.String(), warn) || !strings.Contains(out.String(), "workspace: ") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestForkChecksBeforeCreatingSandbox(t *testing.T) {
	agent2, script2, db := forkSetup(t, forkSandboxYAML)
	o := forkOptions{runOptions: runOptions{agentPath: agent2, db: db, fakeScript: script2, runID: "f1"}, atStep: 2}
	err := forkAgentWith(context.Background(), o, "o1", &bytes.Buffer{}, func(context.Context) (runBackend, error) {
		t.Fatal("sandbox backend must not be opened")
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "no checkpoint for that step") {
		t.Fatalf("err = %v", err)
	}
}

func TestForkCommandNotCompletedExitsTwo(t *testing.T) {
	agent2, script2, db := forkSetup(t, "")
	if err := os.WriteFile(script2, []byte(maxTokensScript), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := execCLI(t, "fork", "o1", agent2, "--at-step", "1", "--run-id", "f1", "--db", db, "--fake-script", script2)
	if code != 2 || !strings.Contains(out, "forked:  o1 at step 1") {
		t.Fatalf("code %d, stdout %s", code, out)
	}
}
