package files_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
	"github.com/roman-ambinder/agentic-framework/internal/tools/files"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
	"github.com/roman-ambinder/agentic-framework/internal/tools/toolstest"
)

func newSession(t *testing.T) (*mcp.ClientSession, string) {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	host := sandboxtest.NewHost(t.TempDir())
	h, err := host.Create(context.Background(), sandbox.Spec{RunID: "t", Image: "host", WorkspaceDir: src})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := host.WorkspaceDir(h)
	if err != nil {
		t.Fatal(err)
	}
	return toolstest.Connect(t, files.NewServer(sandboxtool.Options{Sandbox: host, Handle: h})), ws
}

func TestFilesWriteReadRoundTrip(t *testing.T) {
	cs, _ := newSession(t)
	var b strings.Builder
	for b.Len() < 200000 {
		b.WriteString("line with $pecial 'quotes' \"dq\" `bt` \\ and ünïcode\n")
	}
	content := b.String()[:200000]
	res := toolstest.Call(t, cs, "write_file", map[string]any{"path": "sub/dir/x.txt", "content": content})
	if res.IsError || toolstest.Text(res) != "wrote 200000 bytes to sub/dir/x.txt" {
		t.Fatalf("write: IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
	got := toolstest.Call(t, cs, "read_file", map[string]any{"path": "sub/dir/x.txt"})
	if got.IsError || toolstest.Text(got) != content {
		t.Fatalf("read back differs (IsError=%v, len %d want %d)", got.IsError, len(toolstest.Text(got)), len(content))
	}
}

func TestFilesWriteReplacesAndHandlesEmpty(t *testing.T) {
	cs, ws := newSession(t)
	toolstest.Call(t, cs, "write_file", map[string]any{"path": "a.txt", "content": ""})
	data, err := os.ReadFile(filepath.Join(ws, "a.txt"))
	if err != nil || len(data) != 0 {
		t.Fatalf("a.txt = %q, err %v; want empty", data, err)
	}
}

func TestFilesWriteRejects(t *testing.T) {
	cs, _ := newSession(t)
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"escape", map[string]any{"path": "../x", "content": "c"}, `path "../x" must be relative to the workspace and must not contain ".."`},
		{"dot", map[string]any{"path": ".", "content": "c"}, "path must name a file"},
		{"too large", map[string]any{"path": "big", "content": strings.Repeat("a", files.MaxWriteBytes+1)}, "content is larger than 1048576 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := toolstest.Call(t, cs, "write_file", tt.args)
			if !res.IsError || !strings.Contains(toolstest.Text(res), tt.want) {
				t.Fatalf("IsError=%v text=%q, want %q", res.IsError, toolstest.Text(res), tt.want)
			}
		})
	}
}

func TestFilesListDir(t *testing.T) {
	cs, _ := newSession(t)
	res := toolstest.Call(t, cs, "list_dir", nil)
	got := toolstest.Text(res)
	if res.IsError || !strings.Contains(got, "a.txt\n") || !strings.Contains(got, "sub/\n") {
		t.Fatalf("IsError=%v list = %q", res.IsError, got)
	}
}

func TestFilesReadMissing(t *testing.T) {
	cs, _ := newSession(t)
	res := toolstest.Call(t, cs, "read_file", map[string]any{"path": "nope.txt"})
	if !res.IsError || !strings.Contains(toolstest.Text(res), "exit_code:") {
		t.Fatalf("IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
	res = toolstest.Call(t, cs, "read_file", map[string]any{"path": "/etc/passwd"})
	if !res.IsError {
		t.Fatal("absolute path accepted")
	}
}

func TestToolNames(t *testing.T) {
	cs, _ := newSession(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	if len(got) != 3 || !got["read_file"] || !got["write_file"] || !got["list_dir"] {
		t.Fatalf("tools = %v", got)
	}
}
