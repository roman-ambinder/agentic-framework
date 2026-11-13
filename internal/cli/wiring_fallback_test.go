package cli

import (
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	modelrouter "github.com/roman-ambinder/agentic-framework/internal/model/router"
)

const fallbackAgent = `name: x
version: 1
model:
  provider: anthropic
  name: m1
  pricing: {input_per_mtok: "1"}
  fallbacks:
    - provider: openai
      name: m2
      pricing: {input_per_mtok: "2"}
policy:
  default: deny
`

func parseAgent(t *testing.T, yaml string) *agentconfig.Agent {
	t.Helper()
	a, err := agentconfig.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBuildModelWithFallbacksReturnsRouter(t *testing.T) {
	m, err := buildModel(parseAgent(t, fallbackAgent), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(*modelrouter.Router); !ok {
		t.Fatalf("model = %T, want *modelrouter.Router", m)
	}
	if m.Provider() != "router" {
		t.Errorf("Provider = %q, want router", m.Provider())
	}
}

func TestBuildModelFallbackFakeNeedsScript(t *testing.T) {
	a := parseAgent(t, "name: x\nversion: 1\nmodel:\n  provider: anthropic\n  name: m1\n  fallbacks:\n    - {provider: fake, name: f1}\npolicy:\n  default: deny\n")
	_, err := buildModel(a, "")
	if err == nil || err.Error() != "run: provider fake needs --fake-script" {
		t.Fatalf("err = %v", err)
	}
}

func TestHarnessConfigCarriesPrices(t *testing.T) {
	cfg := harnessConfig(parseAgent(t, fallbackAgent), "r1", "task")
	if got := cfg.Prices["m2"].InputPerMTok; got != 2_000_000 {
		t.Errorf("Prices[m2].InputPerMTok = %d, want 2000000", got)
	}
	if got := cfg.Prices["m1"].InputPerMTok; got != 1_000_000 {
		t.Errorf("Prices[m1].InputPerMTok = %d, want 1000000", got)
	}
}
