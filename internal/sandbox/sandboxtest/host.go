// Package sandboxtest holds test-only Sandbox implementations and a
// conformance suite every backend can run.
package sandboxtest

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// Host is a Sandbox that runs commands directly on the host, in a temporary
// copy of the workspace. It provides NO isolation. Use it only in tests.
type Host struct {
	// Dir is the parent of all workspaces; set it to t.TempDir().
	Dir string

	mu      sync.Mutex
	next    int
	dirs    map[string]string            // handle id -> workspace dir
	roots   map[string]string            // handle id -> <Dir>/sb-<n>
	envList map[string]map[string]string // handle id -> spec env
}

var _ sandbox.Sandbox = (*Host)(nil)

// NewHost returns a Host that keeps its workspaces under dir.
func NewHost(dir string) *Host {
	return &Host{
		Dir:     dir,
		dirs:    map[string]string{},
		roots:   map[string]string{},
		envList: map[string]map[string]string{},
	}
}

// Create makes a workspace directory and, if spec.WorkspaceDir is set, copies
// that tree into it (regular files and directories only; symlinks are skipped).
func (h *Host) Create(_ context.Context, spec sandbox.Spec) (sandbox.Handle, error) {
	if err := spec.Validate(); err != nil {
		return sandbox.Handle{}, err
	}
	h.mu.Lock()
	h.next++
	n := h.next
	h.mu.Unlock()

	root := filepath.Join(h.Dir, fmt.Sprintf("sb-%d", n))
	ws := filepath.Join(root, "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return sandbox.Handle{}, fmt.Errorf("sandboxtest: create workspace: %w", err)
	}
	if spec.WorkspaceDir != "" {
		if err := copyTree(spec.WorkspaceDir, ws); err != nil {
			return sandbox.Handle{}, fmt.Errorf("sandboxtest: copy workspace: %w", err)
		}
	}
	id := fmt.Sprintf("host-%d", n)
	env := make(map[string]string, len(spec.Env)+1)
	for k, v := range spec.Env {
		env[k] = v
	}
	if _, ok := env["HOME"]; !ok {
		env["HOME"] = sandbox.DefaultHome // same environment shape as Docker
	}
	h.mu.Lock()
	h.dirs[id], h.roots[id], h.envList[id] = ws, root, env
	h.mu.Unlock()
	return sandbox.Handle{ID: id}, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		default:
			return nil // skip symlinks and special files
		}
	})
}

// Exec runs cmd in the workspace directory on the host. Paths inside commands
// must be relative: there is no real /workspace on the host.
func (h *Host) Exec(ctx context.Context, hd sandbox.Handle, cmd []string, timeout time.Duration) (sandbox.ExecResult, error) {
	dir, env, err := h.lookup(hd)
	if err != nil {
		return sandbox.ExecResult{}, err
	}
	if len(cmd) == 0 {
		return sandbox.ExecResult{}, errors.New("sandboxtest: empty command")
	}
	if timeout <= 0 {
		return sandbox.ExecResult{}, errors.New("sandboxtest: exec timeout must be > 0")
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(tctx, cmd[0], cmd[1:]...)
	c.Dir = dir
	c.Env = hostEnv(env)
	c.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr

	start := time.Now()
	runErr := c.Run()
	res := sandbox.ExecResult{
		Duration: time.Since(start),
		TimedOut: errors.Is(tctx.Err(), context.DeadlineExceeded),
		Stdout:   sandbox.CapOutput(stdout.Bytes(), sandbox.MaxOutputBytes),
		Stderr:   sandbox.CapOutput(stderr.Bytes(), sandbox.MaxOutputBytes),
	}
	if err := ctx.Err(); err != nil {
		return sandbox.ExecResult{}, err
	}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		res.ExitCode = exitErr.ExitCode() // -1 if killed by a signal
	case res.TimedOut:
		res.ExitCode = -1 // killed; pipes were closed by WaitDelay
	default:
		return sandbox.ExecResult{}, fmt.Errorf("sandboxtest: run %q: %w", cmd[0], runErr)
	}
	if res.TimedOut && res.ExitCode == 0 {
		res.ExitCode = -1
	}
	return res, nil
}

func hostEnv(env map[string]string) []string {
	out := make([]string, 0, len(env)+1)
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return append(out, "PATH="+os.Getenv("PATH"))
}

func (h *Host) lookup(hd sandbox.Handle) (string, map[string]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	dir, ok := h.dirs[hd.ID]
	if !ok {
		return "", nil, fmt.Errorf("sandboxtest: handle %q: %w", hd.ID, sandbox.ErrNotFound)
	}
	return dir, h.envList[hd.ID], nil
}

// WorkspaceDir returns the host directory that plays /workspace for hd.
func (h *Host) WorkspaceDir(hd sandbox.Handle) (string, error) {
	dir, _, err := h.lookup(hd)
	return dir, err
}

// CopyOut returns a tar archive of path, which must be sandbox.WorkspacePath
// or lie below it. Entry names are relative to the parent of path, as
// `docker cp` produces them (for example workspace/main.go).
func (h *Host) CopyOut(_ context.Context, hd sandbox.Handle, p string) ([]byte, error) {
	ws, _, err := h.lookup(hd)
	if err != nil {
		return nil, err
	}
	if p != sandbox.WorkspacePath && !strings.HasPrefix(p, sandbox.WorkspacePath+"/") {
		return nil, fmt.Errorf("sandboxtest: path %q is outside %s", p, sandbox.WorkspacePath)
	}
	clean := path.Clean(p)
	if clean != sandbox.WorkspacePath && !strings.HasPrefix(clean, sandbox.WorkspacePath+"/") {
		return nil, fmt.Errorf("sandboxtest: path %q is outside %s", p, sandbox.WorkspacePath)
	}
	rel := strings.TrimPrefix(clean, sandbox.WorkspacePath)
	hostPath := filepath.Join(ws, filepath.FromSlash(rel))
	if _, err := os.Lstat(hostPath); err != nil {
		return nil, fmt.Errorf("sandboxtest: copy out %q: %w", p, err)
	}
	return tarPath(hostPath, path.Base(clean))
}

func tarPath(hostPath, base string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(hostPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(hostPath, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = path.Join(base, filepath.ToSlash(rel))
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("sandboxtest: tar: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("sandboxtest: tar: %w", err)
	}
	return buf.Bytes(), nil
}

// Destroy removes the workspace of hd.
func (h *Host) Destroy(_ context.Context, hd sandbox.Handle) error {
	h.mu.Lock()
	root, ok := h.roots[hd.ID]
	if ok {
		delete(h.dirs, hd.ID)
		delete(h.roots, hd.ID)
		delete(h.envList, hd.ID)
	}
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("sandboxtest: handle %q: %w", hd.ID, sandbox.ErrNotFound)
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("sandboxtest: destroy: %w", err)
	}
	return nil
}
