package search_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
	"github.com/roman-ambinder/agentic-framework/internal/tools/search"
	"github.com/roman-ambinder/agentic-framework/internal/tools/toolstest"
)

// hostSession serves search over a Host sandbox. grep is forced by hiding rg:
// the tests only assert behaviour both programs share.
func hostSession(t *testing.T, files map[string]string) (*mcp.ClientSession, string) {
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
	ws, err := host.WorkspaceDir(h)
	if err != nil {
		t.Fatal(err)
	}
	return toolstest.Connect(t, search.NewServer(sandboxtool.Options{Sandbox: host, Handle: h})), ws
}

func lines(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") }

func TestGrepFindsMatches(t *testing.T) {
	cs, _ := hostSession(t, map[string]string{"a.txt": "alpha\nbeta needle\n", "b.txt": "needle one\nx\nneedle two\n"})
	res := toolstest.Call(t, cs, "grep", map[string]any{"pattern": "needle"})
	got := lines(toolstest.Text(res))
	if res.IsError || len(got) != 3 {
		t.Fatalf("IsError=%v lines=%q, want 3 lines", res.IsError, got)
	}
	for _, l := range got {
		if !strings.Contains(l, "needle") || strings.Count(l, ":") < 2 {
			t.Fatalf("line %q is not path:line:text", l)
		}
	}
}

func TestGrepNoMatches(t *testing.T) {
	cs, _ := hostSession(t, map[string]string{"a.txt": "alpha\n"})
	res := toolstest.Call(t, cs, "grep", map[string]any{"pattern": "zzz"})
	if res.IsError || toolstest.Text(res) != "no matches" {
		t.Fatalf("IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
}

func TestGrepMaxResults(t *testing.T) {
	cs, _ := hostSession(t, map[string]string{"a.txt": "m\nm\nm\nm\nm\n"})
	res := toolstest.Call(t, cs, "grep", map[string]any{"pattern": "m", "max_results": 2})
	got := lines(toolstest.Text(res))
	if len(got) != 3 || got[2] != "[3 more matching lines not shown]" {
		t.Fatalf("lines = %q", got)
	}
}

func TestGrepBadInput(t *testing.T) {
	cs, _ := hostSession(t, nil)
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty pattern", map[string]any{"pattern": ""}, "pattern is empty"},
		{"path escape", map[string]any{"pattern": "x", "path": "../x"}, "must be relative to the workspace"},
		{"max too big", map[string]any{"pattern": "x", "max_results": 1001}, "max_results must be between 1 and 1000"},
		{"max negative", map[string]any{"pattern": "x", "max_results": -1}, "max_results must be between 1 and 1000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := toolstest.Call(t, cs, "grep", tt.args)
			if !res.IsError || !strings.Contains(toolstest.Text(res), tt.want) {
				t.Fatalf("IsError=%v text=%q, want %q", res.IsError, toolstest.Text(res), tt.want)
			}
		})
	}
}

func TestGrepPatternNotShellInterpreted(t *testing.T) {
	cs, ws := hostSession(t, map[string]string{"a.txt": "alpha\n"})
	toolstest.Call(t, cs, "grep", map[string]any{"pattern": "$(touch pwned)"})
	if _, err := os.Stat(filepath.Join(ws, "pwned")); err == nil {
		t.Fatal("pattern was interpreted by a shell: file pwned exists")
	}
}

func scriptedSession(t *testing.T, rgExit int, grepKey string) (*mcp.ClientSession, *sandboxtest.Scripted) {
	t.Helper()
	sc := &sandboxtest.Scripted{Results: map[string]sandbox.ExecResult{
		"sh -c command -v rg": {ExitCode: rgExit},
		grepKey:               {Stdout: []byte("a.txt:1:x\n")},
	}}
	h, _ := sc.Create(context.Background(), sandbox.Spec{})
	return toolstest.Connect(t, search.NewServer(sandboxtool.Options{Sandbox: sc, Handle: h})), sc
}

func TestGrepUsesGrepWithoutRipgrep(t *testing.T) {
	cs, sc := scriptedSession(t, 1, "grep -rnIE --exclude-dir=.git -i -e foo -- sub")
	res := toolstest.Call(t, cs, "grep", map[string]any{"pattern": "foo", "path": "sub", "ignore_case": true})
	if res.IsError {
		t.Fatalf("unexpected error: %q", toolstest.Text(res))
	}
	want := [][]string{
		{"sh", "-c", "command -v rg"},
		{"grep", "-rnIE", "--exclude-dir=.git", "-i", "-e", "foo", "--", "sub"},
	}
	if got := sc.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestGrepUsesRipgrepWhenPresent(t *testing.T) {
	cs, sc := scriptedSession(t, 0, "rg --line-number --no-heading --color never --hidden --glob !.git -e foo -- .")
	toolstest.Call(t, cs, "grep", map[string]any{"pattern": "foo"})
	toolstest.Call(t, cs, "grep", map[string]any{"pattern": "foo"})
	rg := []string{"rg", "--line-number", "--no-heading", "--color", "never", "--hidden", "--glob", "!.git", "-e", "foo", "--", "."}
	want := [][]string{{"sh", "-c", "command -v rg"}, rg, rg}
	if got := sc.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q (program probed once)", got, want)
	}
}

func TestGrepExitTwoIsToolError(t *testing.T) {
	sc := &sandboxtest.Scripted{Results: map[string]sandbox.ExecResult{
		"sh -c command -v rg":                     {ExitCode: 1},
		"grep -rnIE --exclude-dir=.git -e ( -- .": {ExitCode: 2, Stderr: []byte("bad regex")},
	}}
	cs := toolstest.Connect(t, search.NewServer(sandboxtool.Options{Sandbox: sc}))
	res := toolstest.Call(t, cs, "grep", map[string]any{"pattern": "("})
	if !res.IsError || !strings.Contains(toolstest.Text(res), "bad regex") {
		t.Fatalf("IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
}

func TestToolNames(t *testing.T) {
	cs, _ := hostSession(t, nil)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "grep" {
		t.Fatalf("tools = %v, want [grep]", res.Tools)
	}
}

func TestGrepExitOneWithStderrIsToolError(t *testing.T) {
	sc := &sandboxtest.Scripted{Results: map[string]sandbox.ExecResult{
		"sh -c command -v rg":                     {ExitCode: 1},
		"grep -rnIE --exclude-dir=.git -e x -- .": {ExitCode: 1, Stderr: []byte("trouble")},
	}}
	cs := toolstest.Connect(t, search.NewServer(sandboxtool.Options{Sandbox: sc}))
	res := toolstest.Call(t, cs, "grep", map[string]any{"pattern": "x"})
	if !res.IsError || !strings.Contains(toolstest.Text(res), "trouble") {
		t.Fatalf("IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
}
