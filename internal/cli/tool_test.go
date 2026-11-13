package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMain(m *testing.M) {
	if os.Getenv("ADLC_RUN_AS_CLI") == "1" {
		os.Exit(Execute(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func TestToolFSRequiresRoot(t *testing.T) {
	code, _, stderr := run(t, "tool", "fs")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "root") {
		t.Errorf("stderr %q does not mention root", stderr)
	}
}

func TestToolFSServesStdio(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.Command(os.Args[0], "tool", "fs", "--root", dir)
	cmd.Env = append(os.Environ(), "ADLC_RUN_AS_CLI=1")
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "read_file", Arguments: json.RawMessage(`{"path":"a.txt"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("bad result: %+v", res)
	}
	var out struct{ Content string }
	if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.Content != "hello" {
		t.Fatalf("content = %q", out.Content)
	}
	if err := cs.Close(); err != nil {
		t.Logf("close: %v", err)
	}
}
