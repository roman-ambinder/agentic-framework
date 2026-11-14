package cli

import (
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
)

// The agent file's planning section (task Q.4) must reach the harness.
func TestHarnessConfigPassesPlanning(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want harness.PlanConfig
	}{
		{name: "off by default", yaml: "", want: harness.PlanConfig{}},
		{name: "on", yaml: "planning: {enabled: true, max_tokens: 10}\n", want: harness.PlanConfig{Enabled: true, MaxTokens: 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := agentconfig.Parse([]byte("name: a\nversion: 1\nmodel: {provider: fake}\npolicy: {default: deny}\n" + tt.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if got := harnessConfig(a, "r", "task").Plan; got != tt.want {
				t.Fatalf("Plan = %+v, want %+v", got, tt.want)
			}
		})
	}
}
