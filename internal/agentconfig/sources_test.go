package agentconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDigestFilesVectors(t *testing.T) {
	tests := []struct {
		name  string
		files []SourceFile
		want  string
	}{
		{"none", nil, "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{
			"agent only",
			[]SourceFile{{AgentFileName, []byte("name: x\n")}},
			"sha256:158ee744f1dcedd399b42b7ff90b39fc536fd0b13060a8101532d68f6f3d813a",
		},
		{
			"agent and prompt",
			[]SourceFile{{AgentFileName, []byte("system_file: p.md\n")}, {"p.md", []byte("hi\n")}},
			"sha256:35dba3fcccdffbbba703b9d8f510c8363f7eabd5e17b8e060afa880d8a9a4b5c",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DigestFiles(tt.files); got != tt.want {
				t.Errorf("DigestFiles = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestDigestFilesOrder(t *testing.T) {
	ag, a, b := SourceFile{AgentFileName, []byte("x")}, SourceFile{"a", []byte("1")}, SourceFile{"b", []byte("2")}
	want := DigestFiles([]SourceFile{ag, a, b})
	for _, files := range [][]SourceFile{{ag, b, a}, {a, ag, b}, {b, a, ag}} {
		if got := DigestFiles(files); got != want {
			t.Errorf("order %v: digest %s, want %s", files, got, want)
		}
	}
}

func TestLoadSetsSources(t *testing.T) {
	a, err := Load("testdata/system-file.yaml")
	if err != nil {
		t.Fatal(err)
	}
	agentBytes, _ := os.ReadFile("testdata/system-file.yaml")
	promptBytes, _ := os.ReadFile("testdata/prompt.md")
	want := []SourceFile{{AgentFileName, agentBytes}, {"prompt.md", promptBytes}}
	if !reflect.DeepEqual(a.Sources, want) {
		t.Errorf("Sources = %v, want %v", a.Sources, want)
	}
}

func TestParseSetsSources(t *testing.T) {
	data := []byte("name: tiny\nversion: 1\nmodel:\n  provider: fake\npolicy:\n  default: deny\n")
	a, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []SourceFile{{AgentFileName, data}}
	if !reflect.DeepEqual(a.Sources, want) {
		t.Errorf("Sources = %v, want %v", a.Sources, want)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDigestTracksContentNotName(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/system-file.yaml", filepath.Join(dir, "one.yaml"))
	copyFile(t, "testdata/prompt.md", filepath.Join(dir, "prompt.md"))
	a1, err := Load(filepath.Join(dir, "one.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "one.yaml"), filepath.Join(dir, "two.yaml")); err != nil {
		t.Fatal(err)
	}
	a2, err := Load(filepath.Join(dir, "two.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if a1.Digest() != a2.Digest() {
		t.Errorf("rename changed the digest: %s vs %s", a1.Digest(), a2.Digest())
	}
	if !strings.HasPrefix(a1.Digest(), "sha256:") {
		t.Errorf("digest %q has no sha256: prefix", a1.Digest())
	}
	p, _ := os.ReadFile(filepath.Join(dir, "prompt.md"))
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), append(p, '!'), 0o644); err != nil {
		t.Fatal(err)
	}
	a3, err := Load(filepath.Join(dir, "two.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if a3.Digest() == a2.Digest() {
		t.Error("changing the prompt did not change the digest")
	}
}

func TestLoadAtUsesWorkDir(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(b, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "p.md"), []byte("be kind\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yml := "name: x\nversion: 1\nmodel:\n  provider: fake\nsystem_file: p.md\npolicy:\n  default: deny\nsandbox:\n  workspace: ./ws\n"
	path := filepath.Join(a, "agent.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "sandbox.workspace") {
		t.Fatalf("Load error = %v, want sandbox.workspace problem", err)
	}
	ag, err := LoadAt(path, b)
	if err != nil {
		t.Fatal(err)
	}
	if ag.Dir != b {
		t.Errorf("Dir = %q, want %q", ag.Dir, b)
	}
	if ag.System != "be kind\n" {
		t.Errorf("System = %q", ag.System)
	}
	if got, want := ag.SandboxWorkspace(), filepath.Join(b, "ws"); got != want {
		t.Errorf("SandboxWorkspace = %q, want %q", got, want)
	}
}

func TestDigestCoversPolicyIncludes(t *testing.T) {
	dir := t.TempDir()
	pol := filepath.Join(dir, "pol.yaml")
	if err := os.WriteFile(pol, []byte("rules:\n  - id: r1\n    risk: read\n    decision: allow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	yml := "name: x\nversion: 1\nmodel:\n  provider: fake\npolicy:\n  default: deny\n  include: [pol.yaml]\n"
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	a1, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(a1.Sources) != 2 || a1.Sources[1].Path != "pol.yaml" {
		t.Fatalf("Sources = %v, want agent.yaml then pol.yaml", a1.Sources)
	}
	if err := os.WriteFile(pol, []byte("rules:\n  - id: r1\n    risk: read\n    decision: deny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Digest() == a2.Digest() {
		t.Error("changing an included policy file did not change the digest")
	}
}
