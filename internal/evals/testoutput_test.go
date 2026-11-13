package evals

import (
	"reflect"
	"testing"
)

func outcomeBug(tests ...string) Bug {
	return Bug{Meta: Meta{HiddenTests: tests, TestPackage: "internal/stock"}}
}

func TestHiddenTestArgs(t *testing.T) {
	tests := []struct {
		name string
		bug  Bug
		want []string
	}{
		{
			name: "two tests in a subpackage",
			bug:  Bug{Meta: Meta{HiddenTests: []string{"TestHiddenA", "TestHiddenB"}, TestPackage: "internal/stock"}},
			want: []string{"go", "test", "-v", "-count=1", "-run", "^(TestHiddenA|TestHiddenB)$", "./internal/stock"},
		},
		{
			name: "test package is the fixture root",
			bug:  Bug{Meta: Meta{HiddenTests: []string{"TestHiddenA"}, TestPackage: "."}},
			want: []string{"go", "test", "-v", "-count=1", "-run", "^(TestHiddenA)$", "./."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HiddenTestArgs(tt.bug); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("HiddenTestArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseTestOutput(t *testing.T) {
	bug := outcomeBug("TestHiddenA", "TestHiddenB")
	tests := []struct {
		name        string
		output      string
		wantPassed  []string
		wantFailed  []string
		wantMissing []string
	}{
		{
			name:       "all pass",
			output:     "=== RUN   TestHiddenA\n--- PASS: TestHiddenA (0.00s)\n=== RUN   TestHiddenB\n--- PASS: TestHiddenB (0.00s)\nPASS\nok  \tpkg\t0.01s\n",
			wantPassed: []string{"TestHiddenA", "TestHiddenB"},
		},
		{
			name:       "one pass one fail",
			output:     "--- FAIL: TestHiddenA (0.00s)\n    a_test.go:9: boom\n--- PASS: TestHiddenB (0.00s)\nFAIL\n",
			wantPassed: []string{"TestHiddenB"},
			wantFailed: []string{"TestHiddenA"},
		},
		{
			name:       "subtest lines are ignored",
			output:     "=== RUN   TestHiddenA\n=== RUN   TestHiddenA/sub\n    --- FAIL: TestHiddenA/sub (0.00s)\n--- PASS: TestHiddenA (0.00s)\n--- PASS: TestHiddenB (0.00s)\n",
			wantPassed: []string{"TestHiddenA", "TestHiddenB"},
		},
		{
			name:        "compile error gives no result lines",
			output:      "# example.com/ledger/internal/stock\n./adlc_hidden_test.go:5:2: undefined: Foo\n",
			wantMissing: []string{"TestHiddenA", "TestHiddenB"},
		},
		{
			name:        "a non-hidden test is ignored",
			output:      "--- PASS: TestOther (0.00s)\n--- PASS: TestHiddenA (0.00s)\n",
			wantPassed:  []string{"TestHiddenA"},
			wantMissing: []string{"TestHiddenB"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTestOutput(bug, []byte(tt.output), 0, false)
			if !reflect.DeepEqual(got.Passed, tt.wantPassed) {
				t.Errorf("Passed = %q, want %q", got.Passed, tt.wantPassed)
			}
			if !reflect.DeepEqual(got.Failed, tt.wantFailed) {
				t.Errorf("Failed = %q, want %q", got.Failed, tt.wantFailed)
			}
			if !reflect.DeepEqual(got.Missing, tt.wantMissing) {
				t.Errorf("Missing = %q, want %q", got.Missing, tt.wantMissing)
			}
		})
	}
}

func TestParseTestOutputKeepsTailAndFlags(t *testing.T) {
	bug := outcomeBug("TestHiddenA")
	long := make([]byte, 10000)
	for i := range long {
		long[i] = 'x'
	}
	copy(long[len(long)-5:], "END!!")
	got := ParseTestOutput(bug, long, 3, true)
	if len(got.Output) != 8192 || got.Output[len(got.Output)-5:] != "END!!" {
		t.Errorf("Output = %d bytes, want last 8192 bytes ending in END!!", len(got.Output))
	}
	if got.ExitCode != 3 || !got.TimedOut {
		t.Errorf("ExitCode/TimedOut = %d/%v, want 3/true", got.ExitCode, got.TimedOut)
	}
}

func TestOutcomeAllPassedAllFailed(t *testing.T) {
	tests := []struct {
		name       string
		outcome    TestOutcome
		wantPassed bool
		wantFailed bool
	}{
		{
			name:       "all passed",
			outcome:    TestOutcome{Passed: []string{"A", "B"}},
			wantPassed: true,
		},
		{
			name:    "one failed",
			outcome: TestOutcome{Passed: []string{"A"}, Failed: []string{"B"}},
		},
		{
			name:    "one missing",
			outcome: TestOutcome{Passed: []string{"A"}, Missing: []string{"B"}},
		},
		{
			name:    "all pass lines but non-zero exit",
			outcome: TestOutcome{Passed: []string{"A"}, ExitCode: 1},
		},
		{
			name:    "all pass lines but timed out",
			outcome: TestOutcome{Passed: []string{"A"}, TimedOut: true},
		},
		{
			name:       "all failed",
			outcome:    TestOutcome{Failed: []string{"A", "B"}, ExitCode: 1},
			wantFailed: true,
		},
		{
			name:    "failed and passed is not all failed",
			outcome: TestOutcome{Passed: []string{"A"}, Failed: []string{"B"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.outcome.AllPassed(); got != tt.wantPassed {
				t.Errorf("AllPassed() = %v, want %v", got, tt.wantPassed)
			}
			if got := tt.outcome.AllFailed(); got != tt.wantFailed {
				t.Errorf("AllFailed() = %v, want %v", got, tt.wantFailed)
			}
		})
	}
}
