package sandboxtest

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// RunConformance checks behaviour every Sandbox must have. newSpec returns a
// valid Spec (backends choose the image).
func RunConformance(t *testing.T, sb sandbox.Sandbox, newSpec func() sandbox.Spec) {
	t.Helper()
	ctx := context.Background()

	create := func(t *testing.T, spec sandbox.Spec) sandbox.Handle {
		t.Helper()
		h, err := sb.Create(ctx, spec)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		t.Cleanup(func() { _ = sb.Destroy(context.Background(), h) })
		return h
	}
	run := func(t *testing.T, h sandbox.Handle, timeout time.Duration, argv ...string) sandbox.ExecResult {
		t.Helper()
		res, err := sb.Exec(ctx, h, argv, timeout)
		if err != nil {
			t.Fatalf("Exec %v: %v", argv, err)
		}
		return res
	}

	t.Run("ExecStdoutStderrExitCode", func(t *testing.T) {
		h := create(t, newSpec())
		res := run(t, h, 30*time.Second, "sh", "-c", "echo out; echo err >&2; exit 3")
		if string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" || res.ExitCode != 3 {
			t.Fatalf("got stdout=%q stderr=%q exit=%d", res.Stdout, res.Stderr, res.ExitCode)
		}
	})
	t.Run("ExecWorkingDirIsWorkspace", func(t *testing.T) {
		h := create(t, newSpec())
		res := run(t, h, 30*time.Second, "sh", "-c", "pwd")
		if !strings.HasSuffix(string(res.Stdout), "/workspace\n") {
			t.Fatalf("pwd = %q, want suffix /workspace", res.Stdout)
		}
	})
	t.Run("ExecTimeout", func(t *testing.T) {
		h := create(t, newSpec())
		start := time.Now()
		res := run(t, h, time.Second, "sh", "-c", "sleep 30")
		if d := time.Since(start); d > 10*time.Second {
			t.Fatalf("Exec took %v, want < 10s", d)
		}
		if !res.TimedOut || res.ExitCode == 0 {
			t.Fatalf("TimedOut=%v ExitCode=%d, want true and non-zero", res.TimedOut, res.ExitCode)
		}
	})
	t.Run("ExecOutputIsCapped", func(t *testing.T) {
		h := create(t, newSpec())
		res := run(t, h, 60*time.Second, "sh", "-c", "head -c 9000000 /dev/zero")
		want := sandbox.MaxOutputBytes + len(sandbox.TruncatedMarker)
		if len(res.Stdout) != want || !bytes.HasSuffix(res.Stdout, []byte(sandbox.TruncatedMarker)) {
			t.Fatalf("len(Stdout) = %d, want %d ending with the marker", len(res.Stdout), want)
		}
	})
	t.Run("ExecEnv", func(t *testing.T) {
		spec := newSpec()
		spec.Env = map[string]string{"ADLC_X": "1"}
		h := create(t, spec)
		res := run(t, h, 30*time.Second, "sh", "-c", "echo $ADLC_X")
		if string(res.Stdout) != "1\n" {
			t.Fatalf("Stdout = %q, want %q", res.Stdout, "1\n")
		}
	})
	t.Run("CreateRejectsInvalidSpec", func(t *testing.T) {
		spec := newSpec()
		spec.RunID = ""
		h, err := sb.Create(ctx, spec)
		if err == nil {
			_ = sb.Destroy(ctx, h)
		}
		if !errors.Is(err, sandbox.ErrInvalidSpec) {
			t.Fatalf("Create err = %v, want ErrInvalidSpec", err)
		}
	})
	t.Run("DestroyThenExecFails", func(t *testing.T) {
		h := create(t, newSpec())
		if err := sb.Destroy(ctx, h); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		_, err := sb.Exec(ctx, h, []string{"true"}, 10*time.Second)
		if !errors.Is(err, sandbox.ErrNotFound) {
			t.Fatalf("Exec after Destroy err = %v, want ErrNotFound", err)
		}
	})
}
