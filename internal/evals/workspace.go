package evals

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PrepareOptions controls PrepareWorkspace.
type PrepareOptions struct {
	NoBug          bool // do not apply bug.patch (clean fixture; self-check only)
	WithFix        bool // also apply fix.patch (ignored when NoBug)
	WithHiddenTest bool // also copy hidden_test.go to HiddenTestPath
}

// PrepareWorkspace makes dst a git repository containing the clean fixture
// with bug.patch applied (and, per opts, fix.patch and the hidden test),
// committed as a single commit "base". dst must not exist or be empty.
func (s *Suite) PrepareWorkspace(ctx context.Context, bug Bug, dst string, opts PrepareOptions) error {
	fx, ok := s.Fixtures[bug.Fixture]
	if !ok {
		return fmt.Errorf("evals: %s: fixture %q is not loaded in this suite", bug.ID, bug.Fixture)
	}
	if err := requireEmptyDir(dst); err != nil {
		return err
	}
	if err := copyDir(fx.Dir, dst); err != nil {
		return err
	}
	if err := gitRun(ctx, dst, "init", "-q", "-b", "main"); err != nil {
		return fmt.Errorf("evals: %s: %w", bug.ID, err)
	}
	if !opts.NoBug {
		if err := gitApply(ctx, dst, bug.BugPatch); err != nil {
			return fmt.Errorf("evals: %s: bug.patch does not apply: %w", bug.ID, err)
		}
	}
	if opts.WithFix && !opts.NoBug {
		if err := gitApply(ctx, dst, bug.FixPatch); err != nil {
			return fmt.Errorf("evals: %s: fix.patch does not apply: %w", bug.ID, err)
		}
	}
	if opts.WithHiddenTest {
		p := filepath.Join(dst, HiddenTestPath(bug))
		if err := os.WriteFile(p, bug.HiddenTest, 0o644); err != nil {
			return fmt.Errorf("evals: %s: write hidden test: %w", bug.ID, err)
		}
	}
	return gitCommitBase(ctx, dst)
}

// HiddenTestPath is where the hidden test goes, relative to the workspace root:
// filepath.Join(bug.TestPackage, "adlc_hidden_test.go").
func HiddenTestPath(bug Bug) string {
	return filepath.Join(bug.TestPackage, "adlc_hidden_test.go")
}

// requireEmptyDir returns an error unless dst does not exist or is empty.
func requireEmptyDir(dst string) error {
	entries, err := os.ReadDir(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("evals: workspace %s: %w", dst, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("evals: workspace %s is not empty", dst)
	}
	return nil
}

// copyDir copies the regular files and directories of src into dst, keeping
// file modes. It skips .git directories and refuses symlinks.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" && path != src {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("evals: fixture contains symlink %s", rel)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			return fmt.Errorf("evals: fixture contains irregular file %s", rel)
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// gitApply applies a patch in dir. The patch is written to a temporary file
// outside dir first, so it never appears in the workspace.
func gitApply(ctx context.Context, dir string, patch []byte) error {
	f, err := os.CreateTemp("", "adlc-patch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(patch); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return gitRun(ctx, dir, "apply", "--whitespace=nowarn", f.Name())
}

// gitCommitBase stages everything in dir and makes the single commit "base".
func gitCommitBase(ctx context.Context, dir string) error {
	if err := gitRun(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	return gitRun(ctx, dir,
		"-c", "user.name=adlc", "-c", "user.email=adlc@localhost", "-c", "commit.gpgsign=false",
		"commit", "-q", "-m", "base")
}

// gitRun runs git with args in dir and returns an error carrying its stderr.
func gitRun(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
