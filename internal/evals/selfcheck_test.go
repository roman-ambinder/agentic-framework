//go:build evals

package evals

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSeededBugsBehave checks every seeded bug of evals/suites/go-bugs: the
// clean fixture passes the hidden tests, the bug makes them fail (and they
// compile), and the reference fix makes them pass again. Run with make evals-check.
func TestSeededBugsBehave(t *testing.T) {
	checkSuiteBehaves(t, "../../evals/suites/go-bugs")
}

// TestMiniSuiteBehaves runs the same check on the small testdata suite.
func TestMiniSuiteBehaves(t *testing.T) {
	checkSuiteBehaves(t, "testdata/suites/mini")
}

func checkSuiteBehaves(t *testing.T, dir string) {
	t.Helper()
	suite, err := LoadSuite(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, bug := range suite.Bugs {
		t.Run(bug.ID, func(t *testing.T) {
			t.Parallel()
			checkCleanPasses(t, suite, bug)
			checkBugFails(t, suite, bug)
			checkFixPasses(t, suite, bug)
		})
	}
}

func checkCleanPasses(t *testing.T, suite *Suite, bug Bug) {
	t.Helper()
	ws := prepareSelfCheck(t, suite, bug, PrepareOptions{NoBug: true, WithHiddenTest: true})
	if got := runHiddenTests(t, bug, ws); !got.AllPassed() {
		t.Errorf("clean fixture: hidden tests must pass, got %+v", got)
	}
}

func checkBugFails(t *testing.T, suite *Suite, bug Bug) {
	t.Helper()
	ws := prepareSelfCheck(t, suite, bug, PrepareOptions{WithHiddenTest: true})
	if code := compileTests(t, bug, ws); code != 0 {
		t.Fatalf("buggy tree: hidden test file does not compile (exit %d)", code)
	}
	if got := runHiddenTests(t, bug, ws); !got.AllFailed() {
		t.Errorf("buggy tree: every hidden test must fail, got %+v", got)
	}
}

func checkFixPasses(t *testing.T, suite *Suite, bug Bug) {
	t.Helper()
	ws := prepareSelfCheck(t, suite, bug, PrepareOptions{WithFix: true, WithHiddenTest: true})
	if got := runHiddenTests(t, bug, ws); !got.AllPassed() {
		t.Errorf("fixed tree: hidden tests must pass, got %+v", got)
	}
}

func prepareSelfCheck(t *testing.T, suite *Suite, bug Bug, opts PrepareOptions) string {
	t.Helper()
	ws := filepath.Join(t.TempDir(), "ws")
	if err := suite.PrepareWorkspace(context.Background(), bug, ws, opts); err != nil {
		t.Fatal(err)
	}
	return ws
}

// compileTests runs go test with no tests selected, which compiles the
// package and its test files, and returns the exit code.
func compileTests(t *testing.T, bug Bug, ws string) int {
	t.Helper()
	pkg := "./" + filepath.ToSlash(filepath.Clean(bug.TestPackage))
	out, code := runGo(t, bug.Timeout, ws, "test", "-count=1", "-run", "^$", pkg)
	if code != 0 {
		t.Logf("go test output:\n%s", out)
	}
	return code
}

// runHiddenTests runs the bug's hidden tests in ws and parses the result.
func runHiddenTests(t *testing.T, bug Bug, ws string) TestOutcome {
	t.Helper()
	args := HiddenTestArgs(bug)
	out, code, timedOut := runCmd(t, bug.Timeout, ws, args)
	return ParseTestOutput(bug, out, code, timedOut)
}

func runGo(t *testing.T, timeout time.Duration, ws string, args ...string) ([]byte, int) {
	t.Helper()
	out, code, _ := runCmd(t, timeout, ws, append([]string{"go"}, args...))
	return out, code
}

// runCmd runs args in ws with the timeout and returns combined output, the
// exit code and whether the timeout expired.
func runCmd(t *testing.T, timeout time.Duration, ws string, args []string) ([]byte, int, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "GOWORK=off")
	cmd.WaitDelay = 5 * time.Second // do not hang on a killed test binary's pipes
	out, err := cmd.CombinedOutput()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if err == nil {
		return out, 0, false
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitCode(), timedOut
	}
	if timedOut {
		return out, -1, true
	}
	t.Fatalf("run %v: %v", args, err)
	return nil, 0, false
}
