package sandboxtool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		res  sandbox.ExecResult
		want string
	}{
		{
			"stdout only",
			sandbox.ExecResult{Stdout: []byte("hi\n")},
			"exit_code: 0\n--- stdout ---\nhi\n--- stderr ---\n(empty)\n",
		},
		{
			"failure with both",
			sandbox.ExecResult{ExitCode: 1, Stdout: []byte("out"), Stderr: []byte("err\n")},
			"exit_code: 1\n--- stdout ---\nout\n--- stderr ---\nerr\n",
		},
		{
			"timed out",
			sandbox.ExecResult{ExitCode: -1, TimedOut: true},
			"exit_code: -1\ntimed_out: true\n--- stdout ---\n(empty)\n--- stderr ---\n(empty)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Format(tt.res); got != tt.want {
				t.Fatalf("Format() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckPath(t *testing.T) {
	tests := []struct {
		path string
		ok   bool
	}{
		{".", true},
		{"a.go", true},
		{"dir/a.go", true},
		{"/etc/passwd", false},
		{"../x", false},
		{"a/../../x", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := CheckPath(tt.path)
			if tt.ok {
				if err != nil {
					t.Fatalf("CheckPath(%q) = %v, want nil", tt.path, err)
				}
				return
			}
			want := `path "` + tt.path + `" must be relative to the workspace and must not contain ".."`
			if err == nil || err.Error() != want {
				t.Fatalf("CheckPath(%q) = %v, want %q", tt.path, err, want)
			}
		})
	}
}

type recordSandbox struct {
	sandboxtest.Scripted
	timeout time.Duration
	err     error
}

func (r *recordSandbox) Exec(_ context.Context, _ sandbox.Handle, _ []string, d time.Duration) (sandbox.ExecResult, error) {
	r.timeout = d
	return sandbox.ExecResult{}, r.err
}

func TestRunTimeoutAndError(t *testing.T) {
	rs := &recordSandbox{}
	o := Options{Sandbox: rs}
	if _, err := o.Run(context.Background(), []string{"x"}, 0); err != nil {
		t.Fatal(err)
	}
	if rs.timeout != 2*time.Minute {
		t.Fatalf("default timeout = %v, want 2m", rs.timeout)
	}
	o.DefaultTimeout = 7 * time.Second
	_, _ = o.Run(context.Background(), []string{"x"}, 0)
	if rs.timeout != 7*time.Second {
		t.Fatalf("timeout = %v, want 7s", rs.timeout)
	}
	_, _ = o.Run(context.Background(), []string{"x"}, time.Second)
	if rs.timeout != time.Second {
		t.Fatalf("timeout = %v, want 1s", rs.timeout)
	}
	sentinel := errors.New("boom")
	rs.err = sentinel
	if _, err := o.Run(context.Background(), []string{"x"}, 0); !errors.Is(err, sentinel) {
		t.Fatalf("Run error = %v, want wrapping boom", err)
	}
}
