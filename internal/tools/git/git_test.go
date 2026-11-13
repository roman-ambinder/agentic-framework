package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
	"github.com/roman-ambinder/agentic-framework/internal/tools/git"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
	"github.com/roman-ambinder/agentic-framework/internal/tools/toolstest"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newRepoSession(t *testing.T) (*testingSession, string) {
	t.Helper()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "init", "-q")
	runGit(t, src, "add", ".")
	runGit(t, src, "commit", "-q", "-m", "init")
	host := sandboxtest.NewHost(t.TempDir())
	h, err := host.Create(context.Background(), sandbox.Spec{RunID: "t", Image: "host", WorkspaceDir: src})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := host.WorkspaceDir(h)
	if err != nil {
		t.Fatal(err)
	}
	cs := toolstest.Connect(t, git.NewServer(sandboxtool.Options{Sandbox: host, Handle: h}))
	return &testingSession{cs}, ws
}

func TestGitStatusAndDiff(t *testing.T) {
	s, ws := newRepoSession(t)
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "new.txt"), []byte("brand new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := toolstest.Text(s.call(t, "status", nil))
	if !strings.Contains(status, "a.txt") || !strings.Contains(status, "new.txt") {
		t.Fatalf("status = %q, want a.txt and new.txt", status)
	}
	diff := toolstest.Text(s.call(t, "diff", nil))
	if !strings.Contains(diff, "+two") || strings.Contains(diff, "new.txt") {
		t.Fatalf("diff = %q, want +two and no new.txt", diff)
	}
	staged := toolstest.Text(s.call(t, "diff", map[string]any{"staged": true}))
	if strings.Contains(staged, "+two") {
		t.Fatalf("staged diff = %q, want no unstaged change", staged)
	}
	limited := toolstest.Text(s.call(t, "diff", map[string]any{"path": "a.txt"}))
	if !strings.Contains(limited, "+two") {
		t.Fatalf("path diff = %q, want +two", limited)
	}
}

func TestGitDiffPathCheck(t *testing.T) {
	s, _ := newRepoSession(t)
	res := s.call(t, "diff", map[string]any{"path": "../x"})
	if !res.IsError || !strings.Contains(toolstest.Text(res), "must be relative to the workspace") {
		t.Fatalf("IsError=%v text=%q", res.IsError, toolstest.Text(res))
	}
}

func TestToolNames(t *testing.T) {
	s, _ := newRepoSession(t)
	res, err := s.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	if len(got) != 2 || !got["status"] || !got["diff"] {
		t.Fatalf("tools = %v, want status and diff", got)
	}
}

type testingSession struct{ cs *mcp.ClientSession }

func (s *testingSession) call(t *testing.T, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	return toolstest.Call(t, s.cs, name, args)
}

func TestGitDiffArgv(t *testing.T) {
	sc := &sandboxtest.Scripted{Results: map[string]sandbox.ExecResult{
		"git --no-pager diff --no-color --no-ext-diff --staged -- sub": {},
		"git --no-pager diff --no-color --no-ext-diff":                 {},
	}}
	cs := toolstest.Connect(t, git.NewServer(sandboxtool.Options{Sandbox: sc}))
	toolstest.Call(t, cs, "diff", map[string]any{"path": "sub", "staged": true})
	toolstest.Call(t, cs, "diff", nil)
	want := [][]string{
		{"git", "--no-pager", "diff", "--no-color", "--no-ext-diff", "--staged", "--", "sub"},
		{"git", "--no-pager", "diff", "--no-color", "--no-ext-diff"},
	}
	if got := sc.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}
