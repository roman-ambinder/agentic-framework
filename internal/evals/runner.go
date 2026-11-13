package evals

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// DefaultImage is used when Runner.Image is empty.
const DefaultImage = "golang:1.26"

// goEnv is the only environment the sandboxes get.
var goEnv = map[string]string{
	"GOTOOLCHAIN": "local", "GOFLAGS": "-mod=mod", "GOWORK": "off",
	"GOCACHE": "/tmp/gocache", "HOME": "/tmp", "CGO_ENABLED": "0",
}

// AgentRun is what RunAgent needs to run one agent on one bug.
type AgentRun struct {
	RunID   string
	Agent   *agentconfig.Agent
	Bug     Bug
	Sandbox sandbox.Sandbox
	Handle  sandbox.Handle // sandbox whose /workspace holds the buggy repo
}

// Runner runs bugs through agents.
type Runner struct {
	Suite   *Suite
	Sandbox sandbox.Sandbox
	Log     harness.Log // where RunAgent's events go; Runner only reads
	Image   string      // container image; "" = DefaultImage
	WorkDir string      // host dir for workspaces; required
	// RunAgent runs the harness for one AgentRun and returns the final state.
	// The CLI wires the real harness; tests pass a fake.
	RunAgent func(ctx context.Context, ar AgentRun) (*eventlog.RunState, error)
	Now      func() time.Time // default time.Now
	Stdout   io.Writer        // progress lines; nil = io.Discard
}

// Result is the outcome of one bug x agent.
type Result struct {
	BugID        string             `json:"bug_id"`
	Difficulty   Difficulty         `json:"difficulty"`
	Agent        string             `json:"agent"`    // agent id, e.g. "coder-anthropic@1"
	Provider     string             `json:"provider"` // from the agent file
	RunID        string             `json:"run_id"`
	StopReason   string             `json:"stop_reason"` // adlcv1.StopReason name; "" if the run never started
	Hidden       TestOutcome        `json:"hidden"`
	ChangedFiles []string           `json:"changed_files"`   // relative paths, sorted
	Error        string             `json:"error,omitempty"` // infrastructure failure; Hidden is then empty
	DurationMS   int64              `json:"duration_ms"`
	State        *eventlog.RunState `json:"-"` // for graders (task 1.9)
}

// Solved reports Hidden.AllPassed() && Error == "". Task 1.9 replaces this
// definition with the grader's.
func (r Result) Solved() bool { return r.Error == "" && r.Hidden.AllPassed() }

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) out() io.Writer {
	if r.Stdout != nil {
		return r.Stdout
	}
	return io.Discard
}

func (r *Runner) logf(format string, args ...any) {
	fmt.Fprintf(r.out(), format+"\n", args...)
}

// RunAll runs every bug with every agent, sequentially, bugs in the outer
// loop. It returns an error only when ctx is cancelled, together with the
// results so far.
func (r *Runner) RunAll(ctx context.Context, bugs []Bug, agents []*agentconfig.Agent) ([]Result, error) {
	var results []Result
	for _, bug := range bugs {
		for _, a := range agents {
			if err := ctx.Err(); err != nil {
				return results, err
			}
			results = append(results, r.RunOne(ctx, bug, a))
		}
	}
	return results, nil
}

// runID builds the unique id of one result.
func runID(bug Bug, provider string, t time.Time) string {
	ts := strings.ReplaceAll(t.UTC().Format("20060102T150405.000"), ".", "-")
	return fmt.Sprintf("eval-%s-%s-%s", bug.ID, provider, ts)
}

// RunOne runs bug with agent and grades the result. Problems go into
// Result.Error; it never fails the caller.
func (r *Runner) RunOne(ctx context.Context, bug Bug, agent *agentconfig.Agent) (res Result) {
	start := r.now()
	res = Result{
		BugID: bug.ID, Difficulty: bug.Difficulty, Agent: agent.AgentID(),
		Provider: agent.Model.Provider, RunID: runID(bug, agent.Model.Provider, start),
	}
	defer func() {
		res.DurationMS = r.now().Sub(start).Milliseconds()
		r.logf("%s %s %s %s %ds", bug.ID, res.Provider, resultWord(res), stopWord(res), seconds(res.DurationMS))
	}()
	res.run(ctx, r, bug, agent)
	return res
}

// run executes steps 2-13 of the algorithm and records the outcome in res.
func (res *Result) run(ctx context.Context, r *Runner, bug Bug, agent *agentconfig.Agent) {
	image := r.Image
	if image == "" {
		image = DefaultImage
	}
	base := filepath.Join(r.WorkDir, res.RunID)
	before, after := filepath.Join(base, "before"), filepath.Join(base, "after")
	if err := r.Suite.PrepareWorkspace(ctx, bug, before, PrepareOptions{}); err != nil {
		res.Error = "prepare workspace: " + err.Error()
		return
	}
	spec := sandbox.Spec{RunID: res.RunID, Image: image, WorkspaceDir: before, CPUs: 2, MemoryBytes: 4 << 30, Env: goEnv}
	h, err := r.Sandbox.Create(ctx, spec)
	if err != nil {
		res.Error = "create sandbox: " + err.Error()
		return
	}
	defer r.destroy(ctx, h)

	state, err := r.RunAgent(ctx, AgentRun{RunID: res.RunID, Agent: agent, Bug: bug, Sandbox: r.Sandbox, Handle: h})
	if state != nil {
		res.State = state
		res.StopReason = state.StopReason.String()
	}
	if err != nil {
		res.Error = "run agent: " + err.Error()
		return
	}
	tarData, err := r.Sandbox.CopyOut(ctx, h, sandbox.WorkspacePath)
	if err != nil {
		res.Error = "copy out: " + err.Error()
		return
	}
	if err := extractTar(tarData, after, "workspace"); err != nil {
		res.Error = "extract: " + err.Error()
		return
	}
	res.ChangedFiles = r.changedFiles(ctx, bug, filepath.Join(before, ".git"), after)
	res.Hidden, err = r.grade(ctx, spec, bug, after)
	if err != nil {
		res.Error = err.Error()
	}
}

// destroy removes a sandbox even when ctx is cancelled.
func (r *Runner) destroy(ctx context.Context, h sandbox.Handle) {
	if err := r.Sandbox.Destroy(context.WithoutCancel(ctx), h); err != nil {
		r.logf("destroy sandbox %s: %v", h.ID, err)
	}
}

// grade writes the hidden test into dir and runs it in a fresh sandbox.
func (r *Runner) grade(ctx context.Context, spec sandbox.Spec, bug Bug, dir string) (TestOutcome, error) {
	p := filepath.Join(dir, HiddenTestPath(bug))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return TestOutcome{}, fmt.Errorf("write hidden test: %w", err)
	}
	if err := os.WriteFile(p, bug.HiddenTest, 0o644); err != nil {
		return TestOutcome{}, fmt.Errorf("write hidden test: %w", err)
	}
	spec.RunID += "-grade"
	spec.WorkspaceDir = dir
	h, err := r.Sandbox.Create(ctx, spec)
	if err != nil {
		return TestOutcome{}, fmt.Errorf("create sandbox: %w", err)
	}
	defer r.destroy(ctx, h)
	ex, err := r.Sandbox.Exec(ctx, h, HiddenTestArgs(bug), bug.Timeout)
	if err != nil {
		return TestOutcome{}, fmt.Errorf("run hidden tests: %w", err)
	}
	return ParseTestOutput(bug, append(ex.Stdout, ex.Stderr...), ex.ExitCode, ex.TimedOut), nil
}

// changedFiles lists the files the agent changed relative to the base commit.
// gitDir is the host-side repository that PrepareWorkspace created and that
// never entered the sandbox; workTree is what the agent left behind. Git reads
// its config from gitDir only, so nothing the agent wrote (.git/config hooks,
// filter drivers, fsmonitor) can run a program on the host.
func (r *Runner) changedFiles(ctx context.Context, bug Bug, gitDir, workTree string) []string {
	repo := []string{"--git-dir=" + gitDir, "--work-tree=" + workTree}
	if _, err := gitOutput(ctx, workTree, append(repo, "add", "-A")...); err != nil {
		r.logf("%s git diff failed: %v", bug.ID, err)
		return nil
	}
	out, err := gitOutput(ctx, workTree, append(repo, "diff", "--cached", "--name-only", "HEAD")...)
	if err != nil {
		r.logf("%s git diff failed: %v", bug.ID, err)
		return nil
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	slices.Sort(files)
	return files
}

// gitOutput runs git in dir and returns stdout. Callers pass --git-dir to a
// trusted repository; program-running config is also switched off as a
// second line of defence.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	// The ceiling stops git from finding a repository above dir when the
	// agent deleted .git.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
