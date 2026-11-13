package evals

import (
	"path/filepath"
	"regexp"
	"strings"
)

// maxOutputBytes is how much of a test run's output a TestOutcome keeps.
const maxOutputBytes = 8192

// TestOutcome is the result of running a bug's hidden tests.
type TestOutcome struct {
	Passed   []string `json:"passed"`  // hidden tests that printed "--- PASS"
	Failed   []string `json:"failed"`  // hidden tests that printed "--- FAIL"
	Missing  []string `json:"missing"` // hidden tests that printed neither (did not compile or did not run)
	ExitCode int      `json:"exit_code"`
	TimedOut bool     `json:"timed_out"`
	Output   string   `json:"output"` // last 8 KiB of combined output
}

// AllPassed reports whether every hidden test passed.
func (o TestOutcome) AllPassed() bool {
	return len(o.Failed) == 0 && len(o.Missing) == 0 && o.ExitCode == 0 && !o.TimedOut
}

// AllFailed reports whether every hidden test ran and failed.
func (o TestOutcome) AllFailed() bool {
	return len(o.Passed) == 0 && len(o.Missing) == 0 && len(o.Failed) > 0
}

// HiddenTestArgs returns the go test command line for the bug, run from the
// workspace root: go test -v -count=1 -run '^(A|B)$' ./<test_package>
func HiddenTestArgs(bug Bug) []string {
	return []string{
		"go", "test", "-v", "-count=1",
		"-run", "^(" + strings.Join(bug.HiddenTests, "|") + ")$",
		"./" + filepath.ToSlash(filepath.Clean(bug.TestPackage)),
	}
}

// resultLine matches a top-level result line of go test -v. Subtests are
// indented and do not match.
var resultLine = regexp.MustCompile(`^--- (PASS|FAIL): (\S+) `)

// ParseTestOutput classifies each of bug.HiddenTests from `go test -v` output.
func ParseTestOutput(bug Bug, output []byte, exitCode int, timedOut bool) TestOutcome {
	results := map[string]string{} // test name -> "PASS" or "FAIL"
	for _, line := range strings.Split(string(output), "\n") {
		if m := resultLine.FindStringSubmatch(line); m != nil {
			results[m[2]] = m[1]
		}
	}
	out := TestOutcome{ExitCode: exitCode, TimedOut: timedOut, Output: tail(output, maxOutputBytes)}
	for _, name := range bug.HiddenTests {
		switch results[name] {
		case "PASS":
			out.Passed = append(out.Passed, name)
		case "FAIL":
			out.Failed = append(out.Failed, name)
		default:
			out.Missing = append(out.Missing, name)
		}
	}
	return out
}

// tail returns the last n bytes of b as a string.
func tail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}
