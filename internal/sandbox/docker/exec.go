package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

const (
	execBackstop     = 10 * time.Second
	inspectPollEvery = 50 * time.Millisecond
	inspectPollMax   = 2 * time.Second
)

// Exec runs cmd in the container with working directory sandbox.WorkspacePath.
//
// The command is wrapped in `timeout -s KILL <secs>` so that it is killed
// inside the container when the timeout expires (a context deadline alone
// does not stop the process). The image must therefore provide `timeout`
// (busybox and coreutils do). See sandbox.Sandbox.Exec for the semantics.
func (b *Backend) Exec(ctx context.Context, h sandbox.Handle, cmd []string, timeout time.Duration) (sandbox.ExecResult, error) {
	if timeout <= 0 {
		return sandbox.ExecResult{}, errors.New("docker: exec timeout must be > 0")
	}
	if len(cmd) == 0 {
		return sandbox.ExecResult{}, errors.New("docker: exec command is empty")
	}
	// The handle may come from FindByRunID in another process, so an
	// unknown spec is fine: the container already has its environment.
	var env []string
	if spec, ok := b.spec(h.ID); ok {
		env = envList(spec.Env)
	}
	secs := int(math.Ceil(timeout.Seconds()))
	wrapped := append([]string{"timeout", "-s", "KILL", strconv.Itoa(secs)}, cmd...)

	execCtx, cancel := context.WithTimeout(ctx, timeout+execBackstop)
	defer cancel()

	created, err := b.cli.ExecCreate(execCtx, h.ID, client.ExecCreateOptions{
		Cmd:          wrapped,
		Env:          env,
		WorkingDir:   sandbox.WorkspacePath,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return sandbox.ExecResult{}, fmt.Errorf("docker: exec in %s: %w", shortID(h.ID), errors.Join(sandbox.ErrNotFound, err))
		}
		return sandbox.ExecResult{}, execError(ctx, err)
	}

	start := time.Now()
	stdout, stderr, err := b.attachAndRead(execCtx, created.ID)
	if err != nil {
		return sandbox.ExecResult{}, execError(ctx, err)
	}
	code, err := b.exitCode(execCtx, created.ID)
	if err != nil {
		return sandbox.ExecResult{}, execError(ctx, err)
	}
	elapsed := time.Since(start)
	if err := ctx.Err(); err != nil {
		return sandbox.ExecResult{}, err
	}
	return sandbox.ExecResult{
		ExitCode: code,
		Stdout:   sandbox.CapOutput(stdout.Bytes(), sandbox.MaxOutputBytes),
		Stderr:   sandbox.CapOutput(stderr.Bytes(), sandbox.MaxOutputBytes),
		Duration: elapsed,
		TimedOut: elapsed >= timeout && code != 0,
	}, nil
}

// execError returns the caller's context error if it is done, else wraps err.
func execError(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return fmt.Errorf("docker: exec: %w", err)
}

// attachAndRead starts the exec (ExecAttach starts it; do not call ExecStart)
// and demultiplexes its output.
func (b *Backend) attachAndRead(ctx context.Context, execID string) (stdout, stderr *limitedBuffer, err error) {
	res, err := b.cli.ExecAttach(ctx, execID, client.ExecAttachOptions{})
	if err != nil {
		return nil, nil, err
	}
	defer res.Close()
	// Closing the connection on cancellation unblocks a stuck read.
	stop := context.AfterFunc(ctx, res.Close)
	defer stop()

	stdout = &limitedBuffer{limit: sandbox.MaxOutputBytes + 1}
	stderr = &limitedBuffer{limit: sandbox.MaxOutputBytes + 1}
	if _, err := stdcopy.StdCopy(stdout, stderr, res.Reader); err != nil {
		return nil, nil, fmt.Errorf("read output: %w", err)
	}
	return stdout, stderr, nil
}

// exitCode polls ExecInspect until the exec is no longer running.
func (b *Backend) exitCode(ctx context.Context, execID string) (int, error) {
	deadline := time.Now().Add(inspectPollMax)
	for {
		info, err := b.cli.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return 0, err
		}
		if !info.Running {
			return info.ExitCode, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("exec %s still running after output ended", shortID(execID))
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(inspectPollEvery):
		}
	}
}

// limitedBuffer keeps the first limit bytes written and discards the rest,
// but reports every write as complete so that the writer keeps draining.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

// Write implements io.Writer.
func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.limit - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// Bytes returns what was kept.
func (l *limitedBuffer) Bytes() []byte { return l.buf.Bytes() }
