package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sandboxBase = "name: x\nversion: 1\nmodel:\n  provider: fake\npolicy:\n  default: deny\n"

func TestSandboxSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ws"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "a.yaml")
	yml := sandboxBase + "sandbox:\n  workspace: ./ws\n  cpus: 1.5\n  memory_mb: 512\n  network: true\n" +
		"tools:\n  - server: shell\n    command: [adlc, tool, shell, --sandbox, \"{{sandbox_id}}\"]\n"
	if err := os.WriteFile(file, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := a.SandboxSpec("r1")
	if !ok {
		t.Fatal("no sandbox spec")
	}
	if spec.Image != "golang:1.26" || spec.RunID != "r1" || spec.WorkspaceDir != filepath.Join(a.Dir, "ws") ||
		spec.CPUs != 1.5 || spec.MemoryBytes != 512<<20 || !spec.Network {
		t.Errorf("spec = %+v", spec)
	}
	if err := spec.Validate(); err != nil {
		t.Error(err)
	}
}

func TestNoSandboxSectionHasNoSpec(t *testing.T) {
	a, err := Parse([]byte(sandboxBase))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.SandboxSpec("r1"); ok {
		t.Error("want no spec")
	}
}

func TestSandboxValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := "tools:\n  - server: shell\n    command: [adlc, tool, shell, \"--sandbox={{sandbox_id}}\"]\n"
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{"workspace required", "sandbox:\n  image: x\n", []string{"sandbox.workspace: is required"}},
		{"workspace missing", "sandbox:\n  workspace: " + dir + "/nope\n", []string{"sandbox.workspace:", "not an existing directory"}},
		{"workspace is a file", "sandbox:\n  workspace: " + file + "\n", []string{"sandbox.workspace:", "not an existing directory"}},
		{
			"negative limits reported together", "sandbox:\n  workspace: " + dir + "\n  cpus: -1\n  memory_mb: -5\n",
			[]string{"sandbox.cpus: must be >= 0", "sandbox.memory_mb: must be >= 0"},
		},
		{"placeholder without sandbox", tool, []string{"tools[0].command: uses {{sandbox_id}} but the agent has no sandbox section"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(sandboxBase + tt.yaml))
			if err == nil {
				t.Fatal("want error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestSandboxPlaceholderAllowedWithSection(t *testing.T) {
	dir := t.TempDir()
	yml := sandboxBase + "sandbox:\n  workspace: " + dir + "\ntools:\n  - server: shell\n    command: [adlc, \"{{sandbox_id}}\"]\n"
	if _, err := Parse([]byte(yml)); err != nil {
		t.Fatal(err)
	}
}
