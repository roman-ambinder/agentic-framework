//go:build docker

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// isoSandbox creates a sandbox for spec (RunID and Image are filled in) and
// returns the backend and handle.
func isoSandbox(t *testing.T, spec sandbox.Spec) (*Backend, sandbox.Handle) {
	t.Helper()
	spec.RunID = uniqueRunID(t)
	spec.Image = testImage()
	b := newBackend(t, spec.RunID)
	h, err := b.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return b, h
}

func isoExec(t *testing.T, b *Backend, h sandbox.Handle, cmd ...string) sandbox.ExecResult {
	t.Helper()
	res, err := b.Exec(context.Background(), h, cmd, 60*time.Second)
	if err != nil {
		t.Fatalf("Exec %v: %v", cmd, err)
	}
	return res
}

func writeHostFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDockerNoNetwork(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{})
	res := isoExec(t, b, h, "ls", "/sys/class/net")
	if string(res.Stdout) != "lo\n" {
		t.Fatalf("Stdout = %q, want %q (stderr %q)", res.Stdout, "lo\n", res.Stderr)
	}
}

func TestDockerNetworkWhenAsked(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{Network: true})
	res := isoExec(t, b, h, "ls", "/sys/class/net")
	if !strings.Contains(string(res.Stdout), "eth0") {
		t.Fatalf("Stdout = %q, want it to contain eth0", res.Stdout)
	}
}

func TestDockerRunsAsNonRoot(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{})
	res := isoExec(t, b, h, "id", "-u")
	if string(res.Stdout) != "65532\n" {
		t.Fatalf("Stdout = %q, want 65532", res.Stdout)
	}
}

func TestDockerNoCapabilities(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{})
	res := isoExec(t, b, h, "grep", "CapEff", "/proc/self/status")
	if !strings.Contains(string(res.Stdout), "0000000000000000") {
		t.Fatalf("Stdout = %q, want an empty effective capability set", res.Stdout)
	}
}

func TestDockerMemoryLimit(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{MemoryBytes: 32 << 20})
	res := isoExec(t, b, h, "sh", "-c", `x=$(head -c 100000000 /dev/zero | tr "\0" a); echo ${#x}`)
	if res.ExitCode != 137 {
		t.Fatalf("ExitCode = %d, want 137 (stdout %q, stderr %q)", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestDockerLimitsApplied(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{CPUs: 1.5, MemoryBytes: 64 << 20})
	info, err := b.cli.ContainerInspect(context.Background(), h.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hc := info.Container.HostConfig
	if hc.NanoCPUs != 1_500_000_000 || hc.Memory != 64<<20 || hc.MemorySwap != 64<<20 {
		t.Fatalf("NanoCPUs = %d, Memory = %d, MemorySwap = %d", hc.NanoCPUs, hc.Memory, hc.MemorySwap)
	}
	if hc.PidsLimit == nil || *hc.PidsLimit != sandbox.PidsLimit {
		t.Fatalf("PidsLimit = %v", hc.PidsLimit)
	}
}

func TestDockerWorkspaceCopied(t *testing.T) {
	dir := t.TempDir()
	writeHostFile(t, dir, "a.txt", "hello")
	b, h := isoSandbox(t, sandbox.Spec{WorkspaceDir: dir})
	res := isoExec(t, b, h, "cat", "a.txt")
	if string(res.Stdout) != "hello" {
		t.Fatalf("Stdout = %q, want hello (stderr %q)", res.Stdout, res.Stderr)
	}
}

func TestDockerWorkspaceWritable(t *testing.T) {
	dir := t.TempDir()
	writeHostFile(t, dir, "a.txt", "hello")
	b, h := isoSandbox(t, sandbox.Spec{WorkspaceDir: dir})
	res := isoExec(t, b, h, "sh", "-c", "stat -c %u . && echo x >> a.txt && echo new > new.txt")
	first, _, _ := strings.Cut(string(res.Stdout), "\n")
	if res.ExitCode != 0 || first != "65532" {
		t.Fatalf("ExitCode = %d, Stdout = %q, Stderr = %q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestDockerHostDirUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeHostFile(t, dir, "a.txt", "hello")
	b, h := isoSandbox(t, sandbox.Spec{WorkspaceDir: dir})
	res := isoExec(t, b, h, "sh", "-c", "echo x >> a.txt && echo new > new.txt && rm -f a.txt.bak")
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, Stderr = %q", res.ExitCode, res.Stderr)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil || string(got) != "hello" {
		t.Fatalf("host a.txt = %q, %v; want hello", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("host new.txt exists or stat failed unexpectedly: %v", err)
	}
}

func TestDockerCopyOutHasChanges(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{})
	if res := isoExec(t, b, h, "sh", "-c", "echo new > new.txt"); res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, Stderr = %q", res.ExitCode, res.Stderr)
	}
	data, err := b.CopyOut(context.Background(), h, sandbox.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			t.Fatal("workspace/new.txt not in the archive")
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "workspace/new.txt" {
			body, _ := io.ReadAll(tr)
			if string(body) != "new\n" {
				t.Fatalf("new.txt = %q", body)
			}
			return
		}
	}
}

func TestDockerEmptyWorkspace(t *testing.T) {
	b, h := isoSandbox(t, sandbox.Spec{WorkspaceDir: ""})
	res := isoExec(t, b, h, "pwd")
	if res.ExitCode != 0 || string(res.Stdout) != "/workspace\n" {
		t.Fatalf("ExitCode = %d, Stdout = %q, Stderr = %q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

func TestDockerHostEnvNotLeaked(t *testing.T) {
	t.Setenv("ADLC_HOST_SECRET", "x")
	b, h := isoSandbox(t, sandbox.Spec{})
	res := isoExec(t, b, h, "env")
	if strings.Contains(string(res.Stdout), "ADLC_HOST_SECRET") {
		t.Fatalf("host environment leaked into the sandbox:\n%s", res.Stdout)
	}
}
