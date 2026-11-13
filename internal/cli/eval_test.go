package cli

import (
	"os"
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
