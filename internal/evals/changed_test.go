package evals

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The agent controls everything in the work tree it leaves behind, including
// a .git directory. changedFiles must not let that repository's config run a
// program on the host: here a filter driver and a hook try to create marker.
func TestChangedFilesIgnoresAgentGitConfig(t *testing.T) {
	base := t.TempDir()
	before, after := filepath.Join(base, "before"), filepath.Join(base, "after")
	marker := filepath.Join(base, "pwned")
	for _, d := range []string{before, after} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "a.go"), []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, before, "init", "-q")
	mustGit(t, before, "add", "-A")
	mustGit(t, before, "commit", "-q", "-m", "base")

	// What the agent left: a changed file, and a hostile repository.
	if err := os.WriteFile(filepath.Join(after, "a.go"), []byte("package a // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, after, "init", "-q")
	evil := "[filter \"evil\"]\n\tclean = touch " + marker + "\n[core]\n\tfsmonitor = touch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(after, ".git", "config"), []byte(evil), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(after, ".gitattributes"), []byte("* filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	r := &Runner{Stdout: &log}
	got := r.changedFiles(context.Background(), Bug{}, filepath.Join(before, ".git"), after)

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a program from the agent's .git/config ran on the host")
	}
	if want := []string{".gitattributes", "a.go"}; !slices.Equal(got, want) {
		t.Errorf("changed files = %v, want %v (log: %s)", got, want, log.String())
	}
}
