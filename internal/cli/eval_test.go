package cli

import (
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
