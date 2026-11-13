package evals

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

func TestOtherTestsArgs(t *testing.T) {
	got := OtherTestsArgs(Bug{Meta: Meta{HiddenTests: []string{"TestA", "TestB"}}})
	want := []string{"go", "test", "-count=1", "-skip", "^(TestA|TestB)$", "./..."}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCheckOf(t *testing.T) {
	tests := []struct {
		name   string
		ex     sandbox.ExecResult
		wantOK bool
		output string
	}{
		{"exit 0", sandbox.ExecResult{Stdout: []byte("fine")}, true, ""},
		{"exit 1", sandbox.ExecResult{ExitCode: 1, Stdout: []byte("out "), Stderr: []byte("err")}, false, "out err"},
		{"timed out with exit 0", sandbox.ExecResult{TimedOut: true, Stderr: []byte("slow")}, false, "slow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := checkOf(tt.ex)
			if c.OK != tt.wantOK || c.Output != tt.output || c.ExitCode != tt.ex.ExitCode || c.TimedOut != tt.ex.TimedOut {
				t.Errorf("checkOf = %+v", c)
			}
		})
	}
	t.Run("output keeps the last 4 KiB", func(t *testing.T) {
		c := checkOf(sandbox.ExecResult{ExitCode: 1, Stdout: []byte(strings.Repeat("a", 5000) + "END")})
		if len(c.Output) != 4096 || !strings.HasSuffix(c.Output, "END") {
			t.Errorf("len = %d", len(c.Output))
		}
	})
}

func TestTrajectory(t *testing.T) {
	start := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	st := &eventlog.RunState{
		Step: 7, ModelCalls: 8, ToolCalls: 9, ToolErrors: 2, Denials: 1,
		Usage:     eventlog.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 30, USDMicros: 420_000},
		StartedAt: start, LastEventAt: start.Add(84 * time.Second),
	}
	want := Grade{
		Steps: 7, ModelCalls: 8, ToolCalls: 9, WastedCalls: 2, Denials: 1,
		InputTokens: 100, OutputTokens: 50, CacheReadTokens: 30, USDMicros: 420_000, WallClockMS: 84_000,
	}
	if got := trajectory(st); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := trajectory(&eventlog.RunState{Step: 1, StartedAt: start}); got.WallClockMS != 0 || got.Steps != 1 {
		t.Errorf("zero LastEventAt: %+v", got)
	}
	if got := trajectory(nil); got != (Grade{}) {
		t.Errorf("nil state: %+v", got)
	}
}

func TestGradeSolvedNeedsBuildAndOtherTests(t *testing.T) {
	ok, bad := Check{OK: true}, Check{ExitCode: 1}
	tests := []struct {
		name              string
		hiddenFailed      bool
		build, vet, other Check
		wantSolved        bool
	}{
		{"all ok", false, ok, ok, ok, true},
		{"hidden fails", true, ok, ok, ok, false},
		{"build fails", false, bad, ok, ok, false},
		{"other tests fail", false, ok, ok, bad, false},
		{"vet fails alone", false, ok, bad, ok, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Result{Hidden: TestOutcome{Passed: []string{"TestHiddenA"}}}
			if tt.hiddenFailed {
				res.Hidden = TestOutcome{Failed: []string{"TestHiddenA"}, ExitCode: 1}
			}
			g := gradeOf(res, outcomeChecks{Build: tt.build, Vet: tt.vet, OtherTests: tt.other})
			if g.Solved != tt.wantSolved || g.HiddenPass == tt.hiddenFailed {
				t.Errorf("grade = %+v", g)
			}
			res.Grade = &g
			if res.Solved() != tt.wantSolved {
				t.Errorf("Result.Solved = %v", res.Solved())
			}
		})
	}
}

func TestGradeCountsTestFiles(t *testing.T) {
	g := gradeOf(Result{ChangedFiles: []string{"a.go", "a_test.go", "b/b_test.go"}}, outcomeChecks{})
	if g.FilesChanged != 3 || g.TestFilesChanged != 2 {
		t.Errorf("FilesChanged = %d, TestFilesChanged = %d", g.FilesChanged, g.TestFilesChanged)
	}
}
