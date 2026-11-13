package sandboxtest

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

func TestHostConformance(t *testing.T) {
	RunConformance(t, NewHost(t.TempDir()), func() sandbox.Spec {
		return sandbox.Spec{RunID: "r1", Image: "host"}
	})
}

func TestHostCopiesWorkspace(t *testing.T) {
	src := t.TempDir()
	orig := filepath.Join(src, "a.txt")
	if err := os.WriteFile(orig, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h := NewHost(t.TempDir())
	hd, err := h.Create(ctx, sandbox.Spec{RunID: "r", Image: "host", WorkspaceDir: src})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Exec(ctx, hd, []string{"cat", "a.txt"}, 10*time.Second)
	if err != nil || string(res.Stdout) != "hello\n" {
		t.Fatalf("cat: %q, %v", res.Stdout, err)
	}
	if _, err := h.Exec(ctx, hd, []string{"sh", "-c", "echo changed > a.txt"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(orig)
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("original = %q, %v; want unchanged", got, err)
	}
}

func TestHostCopyOut(t *testing.T) {
	ctx := context.Background()
	h := NewHost(t.TempDir())
	hd, err := h.Create(ctx, sandbox.Spec{RunID: "r", Image: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(ctx, hd, []string{"sh", "-c", "echo hi > b.txt"}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	data, err := h.CopyOut(ctx, hd, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	found := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "workspace/b.txt" {
			body, _ := io.ReadAll(tr)
			if string(body) != "hi\n" {
				t.Fatalf("content = %q", body)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("workspace/b.txt not in archive")
	}
	if _, err := h.CopyOut(ctx, hd, "/etc"); err == nil {
		t.Fatal("CopyOut outside workspace succeeded")
	}
}

func TestScriptedRecordsCalls(t *testing.T) {
	s := &Scripted{Results: map[string]sandbox.ExecResult{
		"echo hi": {ExitCode: 0, Stdout: []byte("hi\n")},
	}}
	ctx := context.Background()
	res, err := s.Exec(ctx, sandbox.Handle{ID: "scripted"}, []string{"echo", "hi"}, time.Second)
	if err != nil || string(res.Stdout) != "hi\n" {
		t.Fatalf("Exec = %q, %v", res.Stdout, err)
	}
	if _, err := s.Exec(ctx, sandbox.Handle{}, []string{"nope"}, time.Second); err == nil {
		t.Fatal("unmapped command did not fail")
	}
	calls := s.Calls()
	if len(calls) != 2 || calls[0][0] != "echo" || calls[1][0] != "nope" {
		t.Fatalf("Calls = %v", calls)
	}
}

func TestHostSetsDefaultHome(t *testing.T) {
	ctx := context.Background()
	h := NewHost(t.TempDir())
	for _, tt := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", nil, sandbox.DefaultHome + "\n"},
		{"explicit", map[string]string{"HOME": "/home/x"}, "/home/x\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hd, err := h.Create(ctx, sandbox.Spec{RunID: "r", Image: "host", Env: tt.env})
			if err != nil {
				t.Fatal(err)
			}
			res, err := h.Exec(ctx, hd, []string{"sh", "-c", "echo $HOME"}, 10*time.Second)
			if err != nil || string(res.Stdout) != tt.want {
				t.Fatalf("HOME = %q, %v; want %q", res.Stdout, err, tt.want)
			}
		})
	}
}
