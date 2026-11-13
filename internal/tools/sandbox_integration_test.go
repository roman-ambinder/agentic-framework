//go:build docker

package tools_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/docker"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxset"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
)

func testImage() string {
	if img := os.Getenv("ADLC_SANDBOX_TEST_IMAGE"); img != "" {
		return img
	}
	return "golang:1.26"
}

func TestDockerShellGitFilesEndToEnd(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	b, err := docker.New(ctx)
	if errors.Is(err, docker.ErrUnavailable) {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	runID := "tools-e2e-" + hex.EncodeToString(suffix)
	t.Cleanup(func() {
		_, _ = b.Cleanup(context.Background(), runID)
		_ = b.Close()
	})
	h, err := b.Create(ctx, sandbox.Spec{RunID: runID, Image: testImage(), WorkspaceDir: src})
	if err != nil {
		t.Fatal(err)
	}
	r, err := sandboxset.NewRouter(ctx, sandboxtool.Options{Sandbox: b, Handle: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	call := func(name, args string) string {
		t.Helper()
		res, err := r.Call(ctx, model.ToolCall{ID: "1", Name: name, Arguments: json.RawMessage(args)})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s failed: %s", name, res.Output)
		}
		return string(res.Output)
	}
	call("files__write_file", `{"path":"pkg/new.go","content":"package pkg\n"}`)
	if out := call("shell__run", `{"command":"go version"}`); !strings.Contains(out, "go version") {
		t.Fatalf("go version output = %q", out)
	}
	if out := call("git__status", `{}`); !strings.Contains(out, "pkg/new.go") {
		t.Fatalf("git status = %q, want pkg/new.go", out)
	}
}
