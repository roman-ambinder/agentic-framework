package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const miniSuite = "../evals/testdata/suites/mini"

func TestEvalRequiresAgents(t *testing.T) {
	code, _, stderr := run(t, "eval", miniSuite)
	if code != 1 || !strings.Contains(stderr, "agents") {
		t.Errorf("code = %d, stderr = %q", code, stderr)
	}
}

func TestEvalUnknownBug(t *testing.T) {
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
	yml := "name: " + name + "\nversion: 1\nmodel: {provider: fake, name: m}\n" + budget + "policy: {default: deny}\n"
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
	if want := "bugs=1 agents=2 worst_case_usd=2.500000\n"; stdout != want {
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
