package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/evals"
)

const miniSuite = "../evals/testdata/suites/mini"

func TestEvalRequiresAgents(t *testing.T) {
	code, _, stderr := run(t, "eval", miniSuite)
	if code != 1 || !strings.Contains(stderr, "agents") {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}

func TestEvalUnknownBug(t *testing.T) {
	t.Setenv("ADLC_ANTHROPIC_MODEL", "test-model")
	db := t.TempDir() + "/events.db"
	code, _, stderr := run(t, "eval", miniSuite, "--agents", "testdata/eval-agent.yaml", "--bugs", "nope", "--db", db)
	if code != 1 || !strings.Contains(stderr, `unknown bug "nope"`) {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}

func TestEvalMissingAgentFile(t *testing.T) {
	code, _, stderr := run(t, "eval", miniSuite, "--agents", "testdata/missing.yaml")
	if code != 1 || stderr == "" {
		t.Errorf("missing agent file: code = %d, stderr = %q", code, stderr)
	}
}

func writeBudgetAgent(t *testing.T, name, maxUSD string) string {
	t.Helper()
	budget := ""
	if maxUSD != "" {
		budget = "budget: {max_usd: \"" + maxUSD + "\"}\n"
	}
	path := filepath.Join(t.TempDir(), name+".yaml")
	// adlc eval rejects provider fake, so dry-run agents use a real provider
	// with a test model name (no model is called in a dry run).
	t.Setenv("ADLC_ANTHROPIC_MODEL", "test-model")
	yml := "name: " + name + "\nversion: 1\nmodel: {provider: anthropic, name: \"${ADLC_ANTHROPIC_MODEL}\"}\n" + budget + "policy: {default: deny}\n"
	if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEvalDryRunWorstCase(t *testing.T) {
	a1 := writeBudgetAgent(t, "one", "2.00")
	a2 := writeBudgetAgent(t, "two", "0.5")
	code, stdout, stderr := run(t, "eval", miniSuite, "--agents", a1+","+a2, "--dry-run")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if want := "bugs=1 agents=2 repeat=1 worst_case_usd=2.500000\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestEvalDryRunWithoutMaxUSDFails(t *testing.T) {
	a := writeBudgetAgent(t, "nolimit", "")
	code, stdout, stderr := run(t, "eval", miniSuite, "--agents", a, "--dry-run")
	if code != 1 || !strings.Contains(stderr, "no budget.max_usd") || stdout != "" {
		t.Errorf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestEvalDryRunUnknownBug(t *testing.T) {
	a := writeBudgetAgent(t, "one", "1")
	code, _, stderr := run(t, "eval", miniSuite, "--agents", a, "--bugs", "nope", "--dry-run")
	if code != 1 || !strings.Contains(stderr, `unknown bug "nope"`) {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}

func TestEvalRejectsFakeProviderBeforeDocker(t *testing.T) {
	dir := t.TempDir()
	agent := dir + "/fake-agent.yaml"
	if err := os.WriteFile(agent, []byte("name: x\nversion: 1\nmodel:\n  provider: fake\npolicy:\n  default: deny\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "eval", miniSuite, "--agents", agent, "--db", dir+"/events.db")
	want := "eval: agent " + agent + ": provider fake is not supported by adlc eval"
	if code != 1 || !strings.Contains(stderr, want) || strings.Contains(stdout+stderr, "not available") {
		t.Errorf("code = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestEvalDryRunRepeat(t *testing.T) {
	a1 := writeBudgetAgent(t, "one", "2.00")
	a2 := writeBudgetAgent(t, "two", "0.5")
	code, stdout, stderr := run(t, "eval", miniSuite, "--agents", a1+","+a2, "--dry-run", "--repeat", "3")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr)
	}
	if want := "bugs=1 agents=2 repeat=3 worst_case_usd=7.500000\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestEvalRepeatOutOfRange(t *testing.T) {
	a := writeBudgetAgent(t, "one", "1")
	for _, n := range []string{"0", "21"} {
		code, _, stderr := run(t, "eval", miniSuite, "--agents", a, "--dry-run", "--repeat", n)
		if code != 1 || !strings.Contains(stderr, "eval: --repeat must be between 1 and 20") {
			t.Errorf("--repeat %s: code = %d, stderr = %q", n, code, stderr)
		}
	}
}

func TestEvalSandboxImage(t *testing.T) {
	tests := []struct {
		name string
		in   evals.AgentRun
		want *adlcv1.SandboxImage
	}{
		{"empty ref", evals.AgentRun{ImageID: "sha256:abc"}, nil},
		{"ref and id", evals.AgentRun{ImageRef: "golang:1.26", ImageID: "sha256:abc"}, &adlcv1.SandboxImage{Ref: "golang:1.26", Id: "sha256:abc"}},
		{"ref only", evals.AgentRun{ImageRef: "golang:1.26"}, &adlcv1.SandboxImage{Ref: "golang:1.26"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evalSandboxImage(tt.in)
			if !proto.Equal(got, tt.want) || (got == nil) != (tt.want == nil) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
