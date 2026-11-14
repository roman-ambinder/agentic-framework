//go:build docker

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox/docker"
)

// TestRunSandboxEndToEnd runs the sandbox-hello example against a real
// Docker daemon: the shell tool writes a file that must appear in the
// copied-out workspace.
func TestRunSandboxEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if b, err := docker.New(ctx); errors.Is(err, docker.ErrUnavailable) {
		t.Skipf("docker daemon not reachable: %v", err)
	} else if err == nil {
		_ = b.Close()
	}
	image := os.Getenv("ADLC_SANDBOX_TEST_IMAGE")
	if image == "" {
		image = "busybox:1.37"
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("../../examples/sandbox-hello.yaml")
	if err != nil {
		t.Fatal(err)
	}
	yml := strings.Replace(string(src), "golang:1.26", image, 1)
	yml = strings.Replace(yml, "    risk:", "    env:\n      ADLC_RUN_AS_CLI: \"1\"\n    risk:", 1)
	agent := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(agent, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	o := runOptions{
		agentPath: agent, task: "write hello.txt", db: filepath.Join(dir, "events.db"),
		fakeScript: "../../examples/sandbox-hello.fake.json", runID: fmt.Sprintf("e2e-%d", time.Now().UnixNano()),
		workspaceOut: out,
	}
	var stdout bytes.Buffer
	if err := runAgent(ctx, o, &stdout); err != nil {
		t.Fatalf("runAgent: %v\nstdout: %s", err, stdout.String())
	}
	got, err := os.ReadFile(filepath.Join(out, "hello.txt"))
	if err != nil || string(got) != "hello from the sandbox\n" {
		t.Fatalf("hello.txt = %q, err = %v\nstdout: %s", got, err, stdout.String())
	}
}
