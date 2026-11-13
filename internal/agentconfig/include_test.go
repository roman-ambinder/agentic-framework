package agentconfig

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
)

const incAgentHead = "name: inc\nversion: 1\nmodel: {provider: fake}\n"

// writeFiles writes name -> content under a new temp dir and returns the dir.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// agentWith returns agent.yaml text whose policy includes the given files.
func agentWith(includes string, rules string) string {
	return incAgentHead + "policy:\n  default: deny\n  include: [" + includes + "]\n" + rules
}

func ruleIDs(a *Agent) []string {
	var ids []string
	for _, r := range a.PolicyEngine().Rules {
		ids = append(ids, r.ID)
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPolicyIncludeOrder(t *testing.T) {
	a, err := Load("testdata/policy/agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"no-keys", "tmp-reads", "known-files", "docs-writes", "other-writes", "reads"}
	if got := ruleIDs(a); !equalStrings(got, want) {
		t.Errorf("rule ids = %v, want %v", got, want)
	}
	if a.PolicyEngine().Default != adlcv1.Decision_DECISION_DENY {
		t.Errorf("default = %v, want deny", a.PolicyEngine().Default)
	}
}

func TestPolicyIncludeDecisions(t *testing.T) {
	a, err := Load("testdata/policy/agent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	eng := a.PolicyEngine()
	tests := []struct {
		name, tool, args, wantRule string
		risk                       adlcv1.RiskClass
		want                       adlcv1.Decision
	}{
		{"docs write", "fs__write_file", `{"path":"docs/a.md"}`, "docs-writes", adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL, adlcv1.Decision_DECISION_ALLOW},
		{"other write", "fs__write_file", `{"path":"src/a.go"}`, "other-writes", adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL, adlcv1.Decision_DECISION_NEEDS_APPROVAL},
		{"key read", "fs__read_file", `{"path":"docs/k.pem"}`, "no-keys", adlcv1.RiskClass_RISK_CLASS_READ, adlcv1.Decision_DECISION_DENY},
		{"tmp read", "fs__read_file", `{"path":"tmp/x"}`, "tmp-reads", adlcv1.RiskClass_RISK_CLASS_READ, adlcv1.Decision_DECISION_ALLOW},
		{"known file", "fs__read_file", `{"path":"go.mod"}`, "known-files", adlcv1.RiskClass_RISK_CLASS_READ, adlcv1.Decision_DECISION_ALLOW},
		{"plain read", "fs__read_file", `{"path":"a.txt"}`, "reads", adlcv1.RiskClass_RISK_CLASS_READ, adlcv1.Decision_DECISION_ALLOW},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := eng.Authorize(context.Background(), policy.Request{Tool: tt.tool, Risk: tt.risk, ArgumentsJSON: tt.args})
			if err != nil {
				t.Fatal(err)
			}
			if d.RuleID != tt.wantRule || d.Decision != tt.want {
				t.Errorf("got %s/%v, want %s/%v", d.RuleID, d.Decision, tt.wantRule, tt.want)
			}
		})
	}
}

func TestPolicyIncludeMissingFile(t *testing.T) {
	dir := writeFiles(t, map[string]string{"agent.yaml": agentWith("missing.yaml", "")})
	_, err := Load(filepath.Join(dir, "agent.yaml"))
	wantErr(t, err, "policy.include: open ", "no such file or directory")
}

func TestPolicyIncludeCycle(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"agent.yaml": agentWith("a.yaml", ""),
		"a.yaml":     "include: [b.yaml]\n",
		"b.yaml":     "include: [a.yaml]\n",
	})
	_, err := Load(filepath.Join(dir, "agent.yaml"))
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	wantErr(t, err, "policy.include: include cycle: "+a+" -> "+b+" -> "+a)
}

func TestPolicyIncludeSelf(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"agent.yaml": agentWith("a.yaml", ""),
		"a.yaml":     "include: [a.yaml]\n",
	})
	_, err := Load(filepath.Join(dir, "agent.yaml"))
	wantErr(t, err, "include cycle")
}

func TestPolicyIncludeDefaultRejected(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"agent.yaml": agentWith("a.yaml", ""),
		"a.yaml":     "default: allow\n",
	})
	_, err := Load(filepath.Join(dir, "agent.yaml"))
	wantErr(t, err, "decode:", `unknown field "default"`)
}

func TestPolicyIncludeUnknownMatcher(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"agent.yaml": agentWith("a.yaml", ""),
		"a.yaml":     "rules:\n  - id: x\n    args: {path: {suffix: x}}\n    decision: allow\n",
	})
	_, err := Load(filepath.Join(dir, "agent.yaml"))
	wantErr(t, err, `unknown field "suffix"`)
}

func TestPolicyIncludeOnce(t *testing.T) {
	rule := func(id string) string { return "rules:\n  - {id: " + id + ", decision: allow}\n" }
	dir := writeFiles(t, map[string]string{
		"agent.yaml": agentWith("b.yaml, c.yaml", ""),
		"b.yaml":     "include: [d.yaml]\n" + rule("b1"),
		"c.yaml":     "include: [d.yaml]\n" + rule("c1"),
		"d.yaml":     rule("d1"),
	})
	a, err := Load(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"d1", "b1", "c1"}
	if got := ruleIDs(a); !equalStrings(got, want) {
		t.Errorf("rule ids = %v, want %v", got, want)
	}
}

func TestPolicyIncludeDuplicateID(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"agent.yaml": agentWith("a.yaml", "  rules: [{id: x, decision: allow}]\n"),
		"a.yaml":     "rules: [{id: x, decision: allow}]\n",
	})
	_, err := Load(filepath.Join(dir, "agent.yaml"))
	wantErr(t, err, `policy.rules[0].id: duplicate rule id "x"`)
}

func TestPolicyIncludeBadRule(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"agent.yaml":  agentWith("shared.yaml", ""),
		"shared.yaml": "rules:\n  - id: x\n    args: {path: {glob: \"docs/{a,b}\"}}\n    decision: allow\n",
	})
	_, err := Load(filepath.Join(dir, "agent.yaml"))
	wantErr(t, err, "shared.yaml: rules[0].args.path: bad glob")
}

func TestPolicyIncludeEmptyPath(t *testing.T) {
	dir := writeFiles(t, map[string]string{"agent.yaml": agentWith(`""`, "")})
	_, err := Load(filepath.Join(dir, "agent.yaml"))
	wantErr(t, err, "policy.include: empty path")
}
