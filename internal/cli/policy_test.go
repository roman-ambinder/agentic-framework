package cli

import (
	"strings"
	"testing"
)

const policyAgent = "../agentconfig/testdata/policy/agent.yaml"

func TestPolicyCheckPrintsDecision(t *testing.T) {
	code, out, errOut := run(t, "policy", "check", policyAgent, "--tool", "fs__write_file", "--args", `{"path":"docs/a.md"}`)
	want := "decision: allow\nrule:     docs-writes\nreason:   matched rule docs-writes\nrisk:     write_local\n"
	if code != 0 || out != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want stdout %q", code, out, errOut, want)
	}
}

func TestPolicyCheckNeedsApproval(t *testing.T) {
	code, out, errOut := run(t, "policy", "check", policyAgent, "--tool", "fs__write_file", "--args", `{"path":"src/a.go"}`)
	want := "decision: needs_approval\nrule:     other-writes\nreason:   matched rule other-writes\nrisk:     write_local\n"
	if code != 0 || out != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want stdout %q", code, out, errOut, want)
	}
}

func TestPolicyCheckUnknownTool(t *testing.T) {
	code, out, errOut := run(t, "policy", "check", policyAgent, "--tool", "x__y")
	want := "decision: deny\nrule:     unspecified-risk\nreason:   tool has no risk class\nrisk:     unspecified\n"
	if code != 0 || out != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want stdout %q", code, out, errOut, want)
	}
}

func TestPolicyCheckRiskFlag(t *testing.T) {
	code, out, errOut := run(t, "policy", "check", policyAgent, "--tool", "x__y", "--risk", "read", "--args", `{"path":"a"}`)
	want := "decision: allow\nrule:     reads\nreason:   matched rule reads\nrisk:     read\n"
	if code != 0 || out != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want stdout %q", code, out, errOut, want)
	}
}

func TestPolicyCheckBadArgs(t *testing.T) {
	code, _, errOut := run(t, "policy", "check", policyAgent, "--tool", "fs__read_file", "--args", `[1]`)
	if code != 1 || !strings.Contains(errOut, "--args must be a JSON object") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
}

func TestPolicyCheckNeedsTool(t *testing.T) {
	if code, _, _ := run(t, "policy", "check", policyAgent); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
}
