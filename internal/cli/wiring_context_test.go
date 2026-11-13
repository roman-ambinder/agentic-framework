package cli

import (
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
)

// The agent file's context section (task 1.6.1) must reach the harness.
func TestHarnessConfigPassesContextBudget(t *testing.T) {
	a, err := agentconfig.Load("../agentconfig/testdata/full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := harnessConfig(a, "run-1", "task")
	want := a.ContextBudget()
	if cfg.Context.MaxInputTokens != want.MaxInputTokens || want.MaxInputTokens == 0 {
		t.Errorf("Context.MaxInputTokens = %d, want %d (non-zero)", cfg.Context.MaxInputTokens, want.MaxInputTokens)
	}
}
