package shell_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
	"github.com/roman-ambinder/agentic-framework/internal/tools/shell"
	"github.com/roman-ambinder/agentic-framework/internal/tools/toolstest"
)

func newSession(t *testing.T, files map[string]string) *mcp.ClientSession {
	t.Helper()
	src := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host := sandboxtest.NewHost(t.TempDir())
	h, err := host.Create(context.Background(), sandbox.Spec{RunID: "t", Image: "host", WorkspaceDir: src})
	if err != nil {
		t.Fatal(err)
	}
	return toolstest.Connect(t, shell.NewServer(sandboxtool.Options{Sandbox: host, Handle: h}))
}

func TestShellRunSuccess(t *testing.T) {
	cs := newSession(t, nil)
	res := toolstest.Call(t, cs, "run", map[string]any{"command": "echo hi"})
	text := toolstest.Text(res)
	if res.IsError || !strings.Contains(text, "exit_code: 0") || !strings.Contains(text, "hi") {
		t.Fatalf("unexpected result (IsError=%v): %q", res.IsError, text)
	}
	sc, ok := res.StructuredContent.(map[string]any)
	if !ok || sc["exit_code"] != float64(0) {
		t.Fatalf("StructuredContent = %#v, want exit_code 0", res.StructuredContent)
	}
}

func TestShellRunFailureIsToolError(t *testing.T) {
	cs := newSession(t, nil)
	res := toolstest.Call(t, cs, "run", map[string]any{"command": "exit 3"})
	if !res.IsError || !strings.Contains(toolstest.Text(res), "exit_code: 3") {
		t.Fatalf("IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
}

func TestShellRunTimeout(t *testing.T) {
	cs := newSession(t, nil)
	start := time.Now()
	res := toolstest.Call(t, cs, "run", map[string]any{"command": "sleep 5", "timeout_seconds": 1})
	if !res.IsError || !strings.Contains(toolstest.Text(res), "timed_out: true") {
		t.Fatalf("IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("took %v, want < 4s", d)
	}
}

func TestShellRunRejectsBadInput(t *testing.T) {
	cs := newSession(t, nil)
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty command", map[string]any{"command": "  "}, "command is empty"},
		{"timeout too large", map[string]any{"command": "true", "timeout_seconds": 9999}, "timeout_seconds must be between 1 and 600"},
		{"timeout negative", map[string]any{"command": "true", "timeout_seconds": -1}, "timeout_seconds must be between 1 and 600"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := toolstest.Call(t, cs, "run", tt.args)
			if !res.IsError || !strings.Contains(toolstest.Text(res), tt.want) {
				t.Fatalf("IsError=%v text=%q, want %q", res.IsError, toolstest.Text(res), tt.want)
			}
		})
	}
}

func TestShellRunsInWorkspace(t *testing.T) {
	cs := newSession(t, map[string]string{"a.txt": "file-content"})
	res := toolstest.Call(t, cs, "run", map[string]any{"command": "cat a.txt"})
	if res.IsError || !strings.Contains(toolstest.Text(res), "file-content") {
		t.Fatalf("IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
}

func TestToolNames(t *testing.T) {
	cs := newSession(t, nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "run" {
		t.Fatalf("tools = %v, want [run]", res.Tools)
	}
}
