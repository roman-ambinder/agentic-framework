package evals

import (
	"strings"

	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// checkOutputBytes caps Check.Output.
const checkOutputBytes = 4 << 10

// Check is the result of one command run in the grading sandbox.
type Check struct {
	OK       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
	Output   string `json:"output,omitempty"` // last 4 KiB of stdout+stderr, only when !OK
}

// Grade is everything measured about one result.
type Grade struct {
	Solved bool `json:"solved"` // HiddenPass && Build.OK && OtherTests.OK

	// Outcome
	HiddenPass       bool  `json:"hidden_pass"`        // Result.Hidden.AllPassed()
	Build            Check `json:"build"`              // go build ./...
	OtherTests       Check `json:"other_tests"`        // go test -count=1 -skip '^(hidden...)$' ./...
	Vet              Check `json:"vet"`                // go vet ./... (reported, not part of Solved)
	TestFilesChanged int   `json:"test_files_changed"` // ChangedFiles ending in "_test.go"
	FilesChanged     int   `json:"files_changed"`

	// Trajectory (zero when the run never started)
	Steps           int   `json:"steps"` // RunState.Step
	ModelCalls      int   `json:"model_calls"`
	ToolCalls       int   `json:"tool_calls"`
	WastedCalls     int   `json:"wasted_calls"` // RunState.ToolErrors
	Denials         int   `json:"denials"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
	USDMicros       int64 `json:"usd_micros"`
	WallClockMS     int64 `json:"wall_clock_ms"` // LastEventAt - StartedAt
}

// outcomeChecks are the three commands run besides the hidden tests.
type outcomeChecks struct {
	Build, Vet, OtherTests Check
}

// OtherTestsArgs returns the command that runs every test except the bug's
// hidden ones, from the workspace root.
func OtherTestsArgs(bug Bug) []string {
	return []string{"go", "test", "-count=1", "-skip", "^(" + strings.Join(bug.HiddenTests, "|") + ")$", "./..."}
}

// checkOf converts an exec result to a Check.
func checkOf(ex sandbox.ExecResult) Check {
	c := Check{OK: ex.ExitCode == 0 && !ex.TimedOut, ExitCode: ex.ExitCode, TimedOut: ex.TimedOut}
	if !c.OK {
		out := append(append([]byte{}, ex.Stdout...), ex.Stderr...)
		if len(out) > checkOutputBytes {
			out = out[len(out)-checkOutputBytes:]
		}
		c.Output = string(out)
	}
	return c
}

// trajectory fills the trajectory fields of a Grade from state. A nil state
// gives a zero Grade.
func trajectory(state *eventlog.RunState) Grade {
	if state == nil {
		return Grade{}
	}
	g := Grade{
		Steps: int(state.Step), ModelCalls: state.ModelCalls, ToolCalls: state.ToolCalls,
		WastedCalls: state.ToolErrors, Denials: state.Denials,
		InputTokens: state.Usage.InputTokens, OutputTokens: state.Usage.OutputTokens,
		CacheReadTokens: state.Usage.CacheReadTokens, USDMicros: state.Usage.USDMicros,
	}
	if !state.StartedAt.IsZero() && !state.LastEventAt.IsZero() {
		g.WallClockMS = state.LastEventAt.Sub(state.StartedAt).Milliseconds()
	}
	return g
}

// gradeOf combines the hidden outcome in res, the checks and the trajectory.
func gradeOf(res Result, checks outcomeChecks) Grade {
	g := trajectory(res.State)
	g.HiddenPass = res.Hidden.AllPassed()
	g.Build, g.Vet, g.OtherTests = checks.Build, checks.Vet, checks.OtherTests
	g.Solved = g.HiddenPass && g.Build.OK && g.OtherTests.OK
	g.FilesChanged = len(res.ChangedFiles)
	for _, f := range res.ChangedFiles {
		if strings.HasSuffix(f, "_test.go") {
			g.TestFilesChanged++
		}
	}
	return g
}
