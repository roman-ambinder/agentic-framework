package evals

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const miniBug = "ok-001"

// copyMini copies the testdata suite and fixture into a temp dir laid out as
// <tmp>/suites/mini and <tmp>/fixtures/tiny, and returns the suite directory.
func copyMini(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	suite := filepath.Join(root, "suites", "mini")
	if err := copyDir("testdata/suites/mini", suite); err != nil {
		t.Fatal(err)
	}
	if err := copyDir("testdata/fixtures/tiny", filepath.Join(root, "fixtures", "tiny")); err != nil {
		t.Fatal(err)
	}
	return suite
}

// mutate replaces the first old with new in the file at path, which must
// contain old.
func mutate(t *testing.T, path, old, new string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), old) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, new, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSuiteMini(t *testing.T) {
	s, err := LoadSuite("testdata/suites/mini")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Bugs) != 1 {
		t.Fatalf("got %d bugs, want 1", len(s.Bugs))
	}
	b := s.Bugs[0]
	if b.ID != miniBug {
		t.Errorf("ID = %q, want %q", b.ID, miniBug)
	}
	if b.Difficulty != Easy {
		t.Errorf("Difficulty = %q, want easy", b.Difficulty)
	}
	if b.Timeout != time.Minute {
		t.Errorf("Timeout = %v, want 1m", b.Timeout)
	}
	if b.Issue == "" {
		t.Error("Issue is empty")
	}
	if fx := s.Fixtures["tiny"]; !strings.HasSuffix(fx.Dir, filepath.Join("testdata", "fixtures", "tiny")) {
		t.Errorf("Fixtures[tiny].Dir = %q, want suffix testdata/fixtures/tiny", fx.Dir)
	}
	if _, ok := s.Bug(miniBug); !ok {
		t.Errorf("Bug(%q) not found", miniBug)
	}
	if _, ok := s.Bug("nope"); ok {
		t.Error("Bug(nope) found")
	}
}

func TestLoadSuiteValidation(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, suite string)
		want string // substring of the error text
	}{
		{
			name: "id mismatch",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "id: ok-001", "id: ok-002")
			},
			want: `evals: ok-001: id "ok-002" does not match directory name`,
		},
		{
			name: "bad id chars",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "id: ok-001", "id: Bad_1")
				if err := os.Rename(filepath.Join(s, miniBug), filepath.Join(s, "Bad_1")); err != nil {
					t.Fatal(err)
				}
			},
			want: `evals: Bad_1: id "Bad_1" must be lowercase letters, digits and dashes`,
		},
		{
			name: "bad difficulty",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "difficulty: easy", "difficulty: trivial")
			},
			want: `evals: ok-001: difficulty "trivial" must be easy, medium or hard`,
		},
		{
			name: "empty title",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "title: Add subtracts", `title: ""`)
			},
			want: "evals: ok-001: title is empty",
		},
		{
			name: "missing fixture",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "fixture: tiny", "fixture: nope")
			},
			want: `evals: ok-001: fixture "nope" not found`,
		},
		{
			name: "missing test_package",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "test_package: calc", "test_package: nothere")
			},
			want: `evals: ok-001: test_package "nothere" not found in fixture "tiny"`,
		},
		{
			name: "empty hidden_tests",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "hidden_tests:\n  - TestHiddenAddSmall\n", "hidden_tests: []\n")
			},
			want: "evals: ok-001: hidden_tests is empty",
		},
		{
			name: "test name without TestHidden prefix",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "- TestHiddenAddSmall", "- TestAddSmall")
			},
			want: `evals: ok-001: hidden test "TestAddSmall" must match ^TestHidden[A-Za-z0-9_]*$`,
		},
		{
			name: "test name not in file",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "- TestHiddenAddSmall", "- TestHiddenMissing")
			},
			want: `evals: ok-001: hidden test "TestHiddenMissing" not found in hidden_test.go`,
		},
		{
			name: "timeout zero",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "timeout: 1m", "timeout: 0s")
			},
			want: "evals: ok-001: timeout must be between 1s and 10m",
		},
		{
			name: "timeout 11m",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "timeout: 1m", "timeout: 11m")
			},
			want: "evals: ok-001: timeout must be between 1s and 10m",
		},
		{
			name: "empty fix_files",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "fix_files:\n  - calc/calc.go\n", "fix_files: []\n")
			},
			want: "evals: ok-001: fix_files is empty",
		},
		{
			name: "fix file not in fixture",
			edit: func(t *testing.T, s string) {
				mutate(t, filepath.Join(s, miniBug, "meta.yaml"), "- calc/calc.go", "- calc/missing.go")
			},
			want: `evals: ok-001: fix file "calc/missing.go" not found in fixture "tiny"`,
		},
		{
			name: "missing issue.md",
			edit: func(t *testing.T, s string) {
				if err := os.Remove(filepath.Join(s, miniBug, "issue.md")); err != nil {
					t.Fatal(err)
				}
			},
			want: "evals: ok-001: issue.md is missing or empty",
		},
		{
			name: "empty bug.patch",
			edit: func(t *testing.T, s string) {
				if err := os.WriteFile(filepath.Join(s, miniBug, "bug.patch"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "evals: ok-001: bug.patch is missing or empty",
		},
		{
			name: "unknown meta field",
			edit: func(t *testing.T, s string) {
				f := filepath.Join(s, miniBug, "meta.yaml")
				data, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f, append(data, []byte("bogus: 1\n")...), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: `unknown field "bogus"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suite := copyMini(t)
			tt.edit(t, suite)
			_, err := LoadSuite(suite)
			if !errors.Is(err, ErrInvalidSuite) {
				t.Fatalf("err = %v, want ErrInvalidSuite", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadSuiteReportsAllProblems(t *testing.T) {
	suite := copyMini(t)
	// ok-001: timeout 0. ok-002: a copy of it with an empty title.
	mutate(t, filepath.Join(suite, miniBug, "meta.yaml"), "timeout: 1m", "timeout: 0s")
	if err := copyDir(filepath.Join(suite, miniBug), filepath.Join(suite, "ok-002")); err != nil {
		t.Fatal(err)
	}
	mutate(t, filepath.Join(suite, "ok-002", "meta.yaml"), "id: ok-001", "id: ok-002")
	mutate(t, filepath.Join(suite, "ok-002", "meta.yaml"), "title: Add subtracts", `title: ""`)

	_, err := LoadSuite(suite)
	if !errors.Is(err, ErrInvalidSuite) {
		t.Fatalf("err = %v, want ErrInvalidSuite", err)
	}
	for _, want := range []string{
		"evals: ok-001: timeout must be between 1s and 10m",
		"evals: ok-002: title is empty",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}

func TestLoadSuiteEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("no bugs here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadSuite(dir)
	if !errors.Is(err, ErrInvalidSuite) || !strings.Contains(err.Error(), "evals: suite has no bugs") {
		t.Fatalf("err = %v, want ErrInvalidSuite with \"evals: suite has no bugs\"", err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func miniSuite(t *testing.T) (*Suite, Bug) {
	t.Helper()
	s, err := LoadSuite("testdata/suites/mini")
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Bugs[0]
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestPrepareWorkspaceAppliesBug(t *testing.T) {
	requireGit(t)
	s, bug := miniSuite(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := s.PrepareWorkspace(context.Background(), bug, ws, PrepareOptions{}); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(ws, "calc", "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "a - b") {
		t.Errorf("calc.go does not contain the bug:\n%s", src)
	}
	if log := gitOut(t, ws, "log", "--oneline"); len(strings.Split(strings.TrimSpace(log), "\n")) != 1 {
		t.Errorf("git log has more than one commit:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(ws, HiddenTestPath(bug))); !os.IsNotExist(err) {
		t.Errorf("hidden test present without WithHiddenTest: %v", err)
	}
}

func TestPrepareWorkspaceWithFixAndHiddenTest(t *testing.T) {
	requireGit(t)
	s, bug := miniSuite(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := s.PrepareWorkspace(context.Background(), bug, ws, PrepareOptions{WithFix: true, WithHiddenTest: true}); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(ws, "calc", "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "a + b") {
		t.Errorf("calc.go does not contain the fix:\n%s", src)
	}
	if _, err := os.Stat(filepath.Join(ws, "calc", "adlc_hidden_test.go")); err != nil {
		t.Errorf("hidden test not written: %v", err)
	}
}

func TestPrepareWorkspaceRejectsNonEmptyDir(t *testing.T) {
	requireGit(t)
	s, bug := miniSuite(t)
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "junk.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := s.PrepareWorkspace(context.Background(), bug, ws, PrepareOptions{})
	if err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("err = %v, want \"is not empty\"", err)
	}
}

func TestPrepareWorkspaceBadPatch(t *testing.T) {
	requireGit(t)
	s, bug := miniSuite(t)
	bug.BugPatch = []byte("this is not a patch\n")
	ws := filepath.Join(t.TempDir(), "ws")
	err := s.PrepareWorkspace(context.Background(), bug, ws, PrepareOptions{})
	if err == nil || !strings.Contains(err.Error(), "bug.patch does not apply") {
		t.Fatalf("err = %v, want \"bug.patch does not apply\"", err)
	}
}

func TestCopyDirRejectsSymlink(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink("/etc/hosts", filepath.Join(src, "link")); err != nil {
		t.Skip("symlinks not supported: ", err)
	}
	err := copyDir(src, filepath.Join(t.TempDir(), "dst"))
	if err == nil || !strings.Contains(err.Error(), "fixture contains symlink") {
		t.Fatalf("err = %v, want \"fixture contains symlink\"", err)
	}
}
