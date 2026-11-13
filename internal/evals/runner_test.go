package evals

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

const fixCmd = "sed -i 's/a - b/a + b/' calc/calc.go"

func completed() *eventlog.RunState {
	return &eventlog.RunState{StopReason: adlcv1.StopReason_STOP_REASON_COMPLETED}
}

func testAgent(t *testing.T, name, provider string) *agentconfig.Agent {
	t.Helper()
	a, err := agentconfig.Parse([]byte("name: " + name + "\nversion: 1\nmodel:\n  provider: " + provider + "\n  name: m\npolicy:\n  default: deny\n"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

type runnerEnv struct {
	r    *Runner
	host *sandboxtest.Host
	bug  Bug
	out  *bytes.Buffer
}

func newRunnerEnv(t *testing.T, fn func(context.Context, AgentRun) (*eventlog.RunState, error)) *runnerEnv {
	t.Helper()
	for _, tool := range []string{"git", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	suite, err := LoadSuite(copyMini(t))
	if err != nil {
		t.Fatal(err)
	}
	host := sandboxtest.NewHost(t.TempDir())
	out := &bytes.Buffer{}
	r := &Runner{Suite: suite, Sandbox: host, WorkDir: t.TempDir(), RunAgent: fn, Stdout: out}
	return &runnerEnv{r: r, host: host, bug: suite.Bugs[0], out: out}
}

func fixing(ctx context.Context, ar AgentRun) (*eventlog.RunState, error) {
	_, err := ar.Sandbox.Exec(ctx, ar.Handle, []string{"sh", "-c", fixCmd}, time.Minute)
	return completed(), err
}

func doNothing(context.Context, AgentRun) (*eventlog.RunState, error) { return completed(), nil }

func TestRunOneSolved(t *testing.T) {
	env := newRunnerEnv(t, fixing)
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	if res.Error != "" {
		t.Fatalf("Error = %q", res.Error)
	}
	if !res.Hidden.AllPassed() || !res.Solved() {
		t.Errorf("not solved: %+v", res.Hidden)
	}
	if len(res.ChangedFiles) != 1 || res.ChangedFiles[0] != "calc/calc.go" {
		t.Errorf("ChangedFiles = %v", res.ChangedFiles)
	}
	if res.StopReason != "STOP_REASON_COMPLETED" || res.Agent != "coder@1" || res.Provider != "anthropic" || res.BugID != env.bug.ID {
		t.Errorf("result = %+v", res)
	}
	if !strings.HasPrefix(res.RunID, "eval-ok-001-anthropic-") || strings.Contains(res.RunID, ".") {
		t.Errorf("RunID = %q", res.RunID)
	}
	if !strings.Contains(env.out.String(), "ok-001 anthropic solved COMPLETED") {
		t.Errorf("progress line missing: %q", env.out.String())
	}
}

func TestRunOneRecordsAgentDigest(t *testing.T) {
	env := newRunnerEnv(t, fixing)
	a := testAgent(t, "coder", "anthropic")
	res := env.r.RunOne(context.Background(), env.bug, a)
	if res.AgentDigest != a.Digest() || !strings.HasPrefix(res.AgentDigest, "sha256:") {
		t.Errorf("AgentDigest = %q, want %q", res.AgentDigest, a.Digest())
	}
}

func TestRunOneGradesSolved(t *testing.T) {
	env := newRunnerEnv(t, fixing)
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	g := res.Grade
	if res.Error != "" || g == nil {
		t.Fatalf("Error = %q, Grade = %+v", res.Error, g)
	}
	if !g.Solved || !g.HiddenPass || !g.Build.OK || !g.OtherTests.OK || !g.Vet.OK {
		t.Errorf("grade = %+v", g)
	}
	if g.FilesChanged != 1 || g.TestFilesChanged != 0 {
		t.Errorf("files = %d, test files = %d", g.FilesChanged, g.TestFilesChanged)
	}
}

func TestRunOneFixThatBreaksBuild(t *testing.T) {
	env := newRunnerEnv(t, func(ctx context.Context, ar AgentRun) (*eventlog.RunState, error) {
		_, err := ar.Sandbox.Exec(ctx, ar.Handle, []string{"sh", "-c", "printf 'package calc\\nfunc Add(a, b int) int { return a +\\n' > calc/calc.go"}, time.Minute)
		return completed(), err
	})
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	g := res.Grade
	if res.Error != "" || g == nil {
		t.Fatalf("Error = %q, Grade = %+v", res.Error, g)
	}
	if g.HiddenPass || g.Build.OK || g.Solved || res.Solved() {
		t.Errorf("grade = %+v", g)
	}
	if g.Build.Output == "" {
		t.Error("failed build has no output")
	}
}

func TestRunOneAddedFailingTest(t *testing.T) {
	env := newRunnerEnv(t, func(ctx context.Context, ar AgentRun) (*eventlog.RunState, error) {
		cmd := fixCmd + " && printf 'package calc\\nimport \"testing\"\\nfunc TestExtra(t *testing.T) { t.Fatal(\"no\") }\\n' > calc/extra_test.go"
		_, err := ar.Sandbox.Exec(ctx, ar.Handle, []string{"sh", "-c", cmd}, time.Minute)
		return completed(), err
	})
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	g := res.Grade
	if res.Error != "" || g == nil {
		t.Fatalf("Error = %q, Grade = %+v", res.Error, g)
	}
	if !g.HiddenPass || g.OtherTests.OK || g.Solved || g.TestFilesChanged != 1 {
		t.Errorf("grade = %+v", g)
	}
}

func TestRunOneChangedFilesSorted(t *testing.T) {
	env := newRunnerEnv(t, func(ctx context.Context, ar AgentRun) (*eventlog.RunState, error) {
		_, err := ar.Sandbox.Exec(ctx, ar.Handle, []string{"sh", "-c", "echo z > z.txt && echo a > a.txt && echo m > calc/m.txt"}, time.Minute)
		return completed(), err
	})
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	want := []string{"a.txt", "calc/m.txt", "z.txt"}
	if !slices.Equal(res.ChangedFiles, want) {
		t.Errorf("ChangedFiles = %v, want %v", res.ChangedFiles, want)
	}
}

func TestRunOneNotSolved(t *testing.T) {
	env := newRunnerEnv(t, doNothing)
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	if res.Error != "" {
		t.Fatalf("Error = %q", res.Error)
	}
	if len(res.Hidden.Failed) != 1 || res.Hidden.Failed[0] != "TestHiddenAddSmall" {
		t.Errorf("Hidden.Failed = %v", res.Hidden.Failed)
	}
	if res.Solved() || len(res.ChangedFiles) != 0 {
		t.Errorf("Solved = %v, ChangedFiles = %v", res.Solved(), res.ChangedFiles)
	}
}

func TestRunOneAgentWorkspaceHasNoHiddenTest(t *testing.T) {
	var problems []string
	env := newRunnerEnv(t, func(ctx context.Context, ar AgentRun) (*eventlog.RunState, error) {
		ls, err := ar.Sandbox.Exec(ctx, ar.Handle, []string{"ls", "calc"}, time.Minute)
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(ls.Stdout), "adlc_hidden_test.go") {
			problems = append(problems, "hidden test present: "+string(ls.Stdout))
		}
		lg, err := ar.Sandbox.Exec(ctx, ar.Handle, []string{"sh", "-c", "git log --oneline | wc -l"}, time.Minute)
		if err != nil {
			return nil, err
		}
		if n := strings.TrimSpace(string(lg.Stdout)); n != "1" {
			problems = append(problems, "commit count "+n)
		}
		return completed(), nil
	})
	env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	for _, p := range problems {
		t.Error(p)
	}
}

func TestRunOneAgentError(t *testing.T) {
	env := newRunnerEnv(t, func(context.Context, AgentRun) (*eventlog.RunState, error) {
		return nil, errors.New("boom")
	})
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	if !strings.HasPrefix(res.Error, "run agent: ") {
		t.Errorf("Error = %q", res.Error)
	}
	if res.Solved() {
		t.Error("Solved with an error")
	}
	if res.DurationMS < 0 {
		t.Error("negative duration")
	}
}

func TestRunOneBudgetExhaustedIsGraded(t *testing.T) {
	env := newRunnerEnv(t, func(context.Context, AgentRun) (*eventlog.RunState, error) {
		return &eventlog.RunState{StopReason: adlcv1.StopReason_STOP_REASON_BUDGET_EXHAUSTED}, nil
	})
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	if res.Error != "" || len(res.Hidden.Failed) != 1 {
		t.Errorf("Error = %q, Hidden = %+v", res.Error, res.Hidden)
	}
	if res.StopReason != "STOP_REASON_BUDGET_EXHAUSTED" {
		t.Errorf("StopReason = %q", res.StopReason)
	}
}

func TestRunAllOrderAndCancel(t *testing.T) {
	env := newRunnerEnv(t, doNothing)
	agents := []*agentconfig.Agent{testAgent(t, "first", "anthropic"), testAgent(t, "second", "openai")}
	results, err := env.r.RunAll(context.Background(), []Bug{env.bug}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Agent != "first@1" || results[1].Agent != "second@1" {
		t.Fatalf("results = %+v", results)
	}
	if results[0].RunID == results[1].RunID {
		t.Error("run ids are not unique")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err = env.r.RunAll(ctx, []Bug{env.bug}, agents)
	if !errors.Is(err, context.Canceled) || len(results) != 0 {
		t.Errorf("got %d results, err %v", len(results), err)
	}
}

func TestRunOneDestroysSandboxes(t *testing.T) {
	env := newRunnerEnv(t, fixing)
	env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	left, err := filepath.Glob(filepath.Join(env.host.Dir, "sb-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("sandboxes left: %v", left)
	}
}

func TestRunOneDeletedAgentGitStillGraded(t *testing.T) {
	env := newRunnerEnv(t, func(ctx context.Context, ar AgentRun) (*eventlog.RunState, error) {
		_, err := ar.Sandbox.Exec(ctx, ar.Handle, []string{"sh", "-c", fixCmd + " && rm -rf .git"}, time.Minute)
		return completed(), err
	})
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "coder", "anthropic"))
	if res.Error != "" || !res.Solved() {
		t.Errorf("Error = %q, Hidden = %+v", res.Error, res.Hidden)
	}
	// Changed files come from the host-side repository, so deleting the
	// agent's .git does not hide what changed.
	if want := []string{"calc/calc.go"}; !slices.Equal(res.ChangedFiles, want) {
		t.Errorf("ChangedFiles = %v, want %v (out = %q)", res.ChangedFiles, want, env.out.String())
	}
}

func TestRunAllRepeat(t *testing.T) {
	env := newRunnerEnv(t, doNothing)
	instant := time.Date(2026, 11, 13, 10, 15, 2, 0, time.UTC)
	env.r.Now = func() time.Time { return instant }
	env.r.Repeat = 3
	agents := []*agentconfig.Agent{testAgent(t, "first", "anthropic"), testAgent(t, "second", "openai")}
	results, err := env.r.RunAll(context.Background(), []Bug{env.bug}, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 6 {
		t.Fatalf("got %d results", len(results))
	}
	seen := map[string]bool{}
	for i, r := range results {
		wantAgent, wantAttempt := "first@1", i%3+1
		if i >= 3 {
			wantAgent = "second@1"
		}
		if r.Agent != wantAgent || r.Attempt != wantAttempt {
			t.Errorf("result %d = %s attempt %d, want %s attempt %d", i, r.Agent, r.Attempt, wantAgent, wantAttempt)
		}
		if !strings.HasSuffix(r.RunID, "-a"+string(rune('0'+wantAttempt))) {
			t.Errorf("run id %q lacks -a%d suffix", r.RunID, wantAttempt)
		}
		seen[r.RunID] = true
	}
	if len(seen) != 6 {
		t.Errorf("run ids not distinct: %v", seen)
	}
}

func TestRunOneIsAttemptOne(t *testing.T) {
	env := newRunnerEnv(t, doNothing)
	res := env.r.RunOne(context.Background(), env.bug, testAgent(t, "first", "anthropic"))
	if res.Attempt != 1 || regexp.MustCompile(`-a[0-9]+$`).MatchString(res.RunID) {
		t.Errorf("attempt %d run id %q", res.Attempt, res.RunID)
	}
}
