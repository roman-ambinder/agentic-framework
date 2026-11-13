package agentconfig

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

func loadFull(t *testing.T) *Agent {
	t.Helper()
	a, err := Load("testdata/full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func wantErr(t *testing.T, err error, subs ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error containing %q, got nil", subs)
	}
	for _, s := range subs {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not contain %q", err, s)
		}
	}
}

func TestLoadFull(t *testing.T) {
	a := loadFull(t)
	if a.AgentID() != "hello@1" {
		t.Errorf("AgentID = %q", a.AgentID())
	}
	wantDir, _ := filepath.Abs("testdata")
	if a.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", a.Dir, wantDir)
	}
	if a.Model.Effort != "high" {
		t.Errorf("Effort = %q", a.Model.Effort)
	}
	if len(a.Tools) != 1 || len(a.Tools[0].Risk) != 3 {
		t.Errorf("tools = %+v", a.Tools)
	}
	if !strings.HasSuffix(a.System, "\n") || !strings.HasPrefix(a.System, "You are") {
		t.Errorf("System = %q", a.System)
	}
}

func TestLoadMinimalDefaults(t *testing.T) {
	a, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if a.Model.Name != "fake-model" || a.Model.MaxTokens != 4096 || a.MaxToolOutputBytes != 16384 {
		t.Errorf("defaults not applied: %+v", a)
	}
	if a.BudgetProto() != nil || len(a.Tools) != 0 {
		t.Error("want no budget and no tools")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	wantErr(t, err, "agentconfig:")
}

func TestUnknownFieldRejected(t *testing.T) {
	_, err := Parse([]byte("name: a\nversion: 1\nmodel: {provider: fake}\nbudget:\n  max_stepz: 3\npolicy: {default: deny}\n"))
	wantErr(t, err, `unknown field "max_stepz"`)
}

func TestValidateReportsAllProblems(t *testing.T) {
	_, err := Parse([]byte(`name: Bad
version: 1
model:
  provider: gpt
  name: m
  max_tokens: -1
budget:
  max_usd: "1.2.3"
policy:
  default: deny
  rules:
    - {id: x, decision: allow}
    - {id: x, decision: allow}
`))
	wantErr(t, err,
		`name: must match ^[a-z][a-z0-9-]{0,31}$, got "Bad"`,
		`model.provider: must be one of fake, anthropic, openai, got "gpt"`,
		`model.max_tokens: must be between 1 and 1000000`,
		`budget.max_usd: "1.2.3" is not a decimal amount with at most 6 decimals`,
		`policy.rules[1].id: duplicate rule id "x"`,
	)
}

const baseYAML = "name: a\nversion: 1\nmodel:\n  provider: fake\n"

func TestValidateTable(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"name", "name: A\nversion: 1\nmodel: {provider: fake}\npolicy: {default: deny}\n", `name: must match`},
		{"version", "name: a\nversion: 0\nmodel: {provider: fake}\npolicy: {default: deny}\n", `version: must be >= 1`},
		{"provider", baseYAML[:len(baseYAML)-5] + "x\npolicy: {default: deny}\n", `model.provider: must be one of fake, anthropic, openai, got "x"`},
		{"model name required", "name: a\nversion: 1\nmodel: {provider: anthropic}\npolicy: {default: deny}\n", `model.name: is required`},
		{"max_tokens", baseYAML + "  max_tokens: 1000001\npolicy: {default: deny}\n", `model.max_tokens: must be between 1 and 1000000`},
		{"effort", baseYAML + "  effort: turbo\npolicy: {default: deny}\n", `model.effort: must be one of low, medium, high, xhigh, max, got "turbo"`},
		{"pricing input", baseYAML + "  pricing: {input_per_mtok: x}\npolicy: {default: deny}\n", `model.pricing.input_per_mtok: "x" is not a decimal amount`},
		{"pricing output", baseYAML + "  pricing: {output_per_mtok: x}\npolicy: {default: deny}\n", `model.pricing.output_per_mtok: "x" is not a decimal amount`},
		{"pricing cache read", baseYAML + "  pricing: {cache_read_per_mtok: x}\npolicy: {default: deny}\n", `model.pricing.cache_read_per_mtok: "x" is not a decimal amount`},
		{"pricing cache write", baseYAML + "  pricing: {cache_write_per_mtok: x}\npolicy: {default: deny}\n", `model.pricing.cache_write_per_mtok: "x" is not a decimal amount`},
		{"max_tool_output_bytes", baseYAML + "max_tool_output_bytes: 10485761\npolicy: {default: deny}\n", `max_tool_output_bytes: must be between 1 and 10485760`},
		{"server pattern", baseYAML + "tools:\n  - {server: A, command: [x]}\npolicy: {default: deny}\n", `tools[0].server: must match ^[a-z][a-z0-9_]{0,15}$ without "__", got "A"`},
		{"server double underscore", baseYAML + "tools:\n  - {server: a__b, command: [x]}\npolicy: {default: deny}\n", `tools[0].server: must match`},
		{"server duplicate", baseYAML + "tools:\n  - {server: a, command: [x]}\n  - {server: a, command: [x]}\npolicy: {default: deny}\n", `tools[1].server: duplicate server "a"`},
		{"command empty", baseYAML + "tools:\n  - {server: a, command: []}\npolicy: {default: deny}\n", `tools[0].command: is required`},
		{"command first empty", baseYAML + "tools:\n  - {server: a, command: [\"\"]}\npolicy: {default: deny}\n", `tools[0].command: is required`},
		{"tool risk", baseYAML + "tools:\n  - {server: a, command: [x], risk: {w: nope}}\npolicy: {default: deny}\n", `tools[0].risk.w: unknown risk class "nope"`},
		{"default_risk", baseYAML + "tools:\n  - {server: a, command: [x], default_risk: nope}\npolicy: {default: deny}\n", `tools[0].default_risk: unknown risk class "nope"`},
		{"wall clock bad", baseYAML + "budget: {max_wall_clock: soon}\npolicy: {default: deny}\n", `budget.max_wall_clock: "soon" is not a positive duration`},
		{"wall clock zero", baseYAML + "budget: {max_wall_clock: 0s}\npolicy: {default: deny}\n", `budget.max_wall_clock: "0s" is not a positive duration`},
		{"max_usd", baseYAML + "budget: {max_usd: \"-1\"}\npolicy: {default: deny}\n", `budget.max_usd: "-1" is not a decimal amount`},
		{"policy default", baseYAML + "policy: {default: maybe}\n", `policy.default: must be one of allow, deny, needs_approval, got "maybe"`},
		{"policy default missing", baseYAML, `policy.default: must be one of`},
		{"rule id required", baseYAML + "policy:\n  default: deny\n  rules: [{decision: allow}]\n", `policy.rules[0].id: is required`},
		{"rule id duplicate", baseYAML + "policy:\n  default: deny\n  rules: [{id: x, decision: allow}, {id: x, decision: allow}]\n", `policy.rules[1].id: duplicate rule id "x"`},
		{"rule id reserved", baseYAML + "policy:\n  default: deny\n  rules: [{id: default, decision: allow}]\n", `policy.rules[0].id: rule id "default" is reserved`},
		{"rule risk", baseYAML + "policy:\n  default: deny\n  rules: [{id: x, risk: nope, decision: allow}]\n", `policy.rules[0].risk: unknown risk class "nope"`},
		{"rule decision", baseYAML + "policy:\n  default: deny\n  rules: [{id: x, decision: nope}]\n", `policy.rules[0].decision: must be one of allow, deny, needs_approval, got "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			wantErr(t, err, tt.want)
		})
	}
}

func TestPrice(t *testing.T) {
	want := model.Price{InputPerMTok: 1500000, OutputPerMTok: 7500000, CacheReadPerMTok: 150000, CacheWritePerMTok: 1875000}
	if got := loadFull(t).Price(); got != want {
		t.Errorf("Price = %+v, want %+v", got, want)
	}
}

func TestBudgetProto(t *testing.T) {
	b := loadFull(t).BudgetProto()
	if b == nil || b.MaxSteps != 20 || b.MaxTokens != 200000 || b.MaxUsdMicros != 1000000 || b.MaxToolCalls != 50 ||
		b.MaxWallClock.AsDuration() != 10*time.Minute {
		t.Errorf("BudgetProto = %v", b)
	}
}

func TestPolicyEngine(t *testing.T) {
	a := loadFull(t)
	s := a.PolicyEngine()
	if len(s.Rules) != 2 || s.Default != adlcv1.Decision_DECISION_DENY {
		t.Fatalf("engine = %+v", s)
	}
	first := s.Rules[0]
	if first.ID != "reads" || first.Risk != adlcv1.RiskClass_RISK_CLASS_READ || first.Decision != adlcv1.Decision_DECISION_ALLOW {
		t.Errorf("first rule = %+v", first)
	}
	a.Policy.Rules = append(a.Policy.Rules, Rule{ID: "any", Decision: "deny"})
	if r := a.PolicyEngine().Rules[2]; r.Risk != adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED {
		t.Errorf("empty risk = %v, want UNSPECIFIED", r.Risk)
	}
}

func TestToolServers(t *testing.T) {
	a := loadFull(t)
	ts := a.ToolServers()
	if len(ts) != 1 {
		t.Fatalf("servers = %d", len(ts))
	}
	s := ts[0]
	if s.Name != "fs" || strings.Join(s.Command, " ") != "adlc tool fs --root ./workspace" || s.Dir != a.Dir {
		t.Errorf("server = %+v", s)
	}
	if len(s.Risk) != 3 || s.Risk["write_file"] != adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL {
		t.Errorf("risk = %v", s.Risk)
	}
	if s.DefaultRisk != adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED {
		t.Errorf("DefaultRisk = %v", s.DefaultRisk)
	}
}

const envYAML = "name: a\nversion: 1\nmodel:\n  provider: anthropic\n  name: ${ADLC_TEST_MODEL}\npolicy: {default: deny}\n"

func TestModelNameFromEnv(t *testing.T) {
	t.Setenv("ADLC_TEST_MODEL", "m-1")
	a, err := Parse([]byte(envYAML))
	if err != nil || a.Model.Name != "m-1" {
		t.Fatalf("got %+v, %v", a, err)
	}
}

func TestModelNameEnvMissing(t *testing.T) {
	_, err := Parse([]byte(envYAML))
	wantErr(t, err, "model.name: environment variable ADLC_TEST_MODEL is not set")
}

func TestSystemFile(t *testing.T) {
	a, err := Load("testdata/system-file.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if a.System != "You are a test prompt.\n" {
		t.Errorf("System = %q", a.System)
	}
}

func TestSystemFileAndSystem(t *testing.T) {
	_, err := Parse([]byte(baseYAML + "system: hi\nsystem_file: prompt.md\npolicy: {default: deny}\n"))
	wantErr(t, err, "system_file: cannot be used together with system")
}

func TestSystemFileMissing(t *testing.T) {
	_, err := Parse([]byte(baseYAML + "system_file: nope.md\npolicy: {default: deny}\n"))
	wantErr(t, err, "system_file:")
}

func TestContextConfig(t *testing.T) {
	tests := []struct{ name, yaml, want string }{
		{"max_input_tokens", baseYAML + "context: {max_input_tokens: -1}\npolicy: {default: deny}\n", "context.max_input_tokens"},
		{"keep_recent_steps", baseYAML + "context: {keep_recent_steps: -1}\npolicy: {default: deny}\n", "context.keep_recent_steps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			wantErr(t, err, tt.want)
		})
	}
}

func TestContextBudget(t *testing.T) {
	a := loadFull(t)
	if b := a.ContextBudget(); b.MaxInputTokens != 150000 || b.KeepRecentSteps != 2 {
		t.Errorf("ContextBudget = %+v", b)
	}
	m, err := Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if b := m.ContextBudget(); b.MaxInputTokens != 0 || b.KeepRecentSteps != 0 {
		t.Errorf("default ContextBudget = %+v", b)
	}
}

// fallbackYAML is a valid agent with one fallback; the names come from the environment.
const fallbackYAML = `name: a
version: 1
model:
  provider: anthropic
  name: ${ADLC_TEST_MODEL}
  pricing: {input_per_mtok: "3", output_per_mtok: "15"}
  fallbacks:
    - provider: openai
      name: ${ADLC_TEST_FALLBACK}
      pricing: {input_per_mtok: "1", output_per_mtok: "2"}
policy: {default: deny}
`

func TestFallbacksConfig(t *testing.T) {
	t.Setenv("ADLC_TEST_MODEL", "primary-1")
	t.Setenv("ADLC_TEST_FALLBACK", "fallback-1")
	t.Run("valid fallback loads", func(t *testing.T) {
		a, err := Parse([]byte(fallbackYAML))
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Model.Fallbacks) != 1 {
			t.Fatalf("fallbacks = %+v, want one", a.Model.Fallbacks)
		}
		f := a.Model.Fallbacks[0]
		if f.Provider != "openai" || f.Name != "fallback-1" || f.MaxTokens != DefaultMaxTokens {
			t.Errorf("fallback = %+v", f)
		}
	})
	tests := []struct{ name, yaml, want string }{
		{"empty name", strings.Replace(fallbackYAML, "name: ${ADLC_TEST_FALLBACK}", "name: \"\"", 1), `model.fallbacks[0].name: is required`},
		{"missing name", strings.Replace(fallbackYAML, "      name: ${ADLC_TEST_FALLBACK}\n", "", 1), `model.fallbacks[0].name: is required`},
		{"env missing", strings.Replace(fallbackYAML, "ADLC_TEST_FALLBACK}", "ADLC_TEST_NOPE}", 1), `model.fallbacks[0].name: environment variable ADLC_TEST_NOPE is not set`},
		{"same as primary", strings.Replace(strings.Replace(fallbackYAML, "provider: openai", "provider: anthropic", 1), "${ADLC_TEST_FALLBACK}", "${ADLC_TEST_MODEL}", 1), `model.fallbacks[0]: repeats the primary provider and name`},
		{"unknown provider", strings.Replace(fallbackYAML, "provider: openai", "provider: nope", 1), `model.fallbacks[0].provider: must be one of fake, anthropic, openai, got "nope"`},
		{"nested fallbacks", strings.Replace(fallbackYAML, "      pricing:", "      fallbacks: [{provider: openai, name: x}]\n      pricing:", 1), `model.fallbacks[0].fallbacks: not allowed`},
		{"bad pricing", strings.Replace(fallbackYAML, `output_per_mtok: "2"`, `output_per_mtok: x`, 1), `model.fallbacks[0].pricing.output_per_mtok: "x" is not a decimal amount`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			wantErr(t, err, tt.want)
		})
	}
}

func TestFallbackPrices(t *testing.T) {
	t.Setenv("ADLC_TEST_MODEL", "primary-1")
	t.Setenv("ADLC_TEST_FALLBACK", "fallback-1")
	a, err := Parse([]byte(fallbackYAML))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]model.Price{
		"primary-1":  {InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000},
		"fallback-1": {InputPerMTok: 1_000_000, OutputPerMTok: 2_000_000},
	}
	if got := a.Prices(); !reflect.DeepEqual(got, want) {
		t.Errorf("Prices() = %+v, want %+v", got, want)
	}
}

func TestFallbackPricesPrimaryWinsOnSharedName(t *testing.T) {
	a, err := Parse([]byte("name: a\nversion: 1\nmodel:\n  provider: anthropic\n  name: shared\n  pricing: {input_per_mtok: \"3\"}\n" +
		"  fallbacks:\n    - {provider: openai, name: shared, pricing: {input_per_mtok: \"1\"}}\npolicy: {default: deny}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Prices()["shared"].InputPerMTok; got != 3_000_000 {
		t.Errorf("Prices[shared].InputPerMTok = %d, want the primary's 3000000", got)
	}
}

const pricingEnvYAML = `name: a
version: 1
model:
  provider: anthropic
  name: m
  pricing:
    input_per_mtok: ${ADLC_TEST_PRICE_IN}
    output_per_mtok: "15"
    cache_read_per_mtok: ${ADLC_TEST_PRICE_CR}
  fallbacks:
    - provider: openai
      name: f
      pricing:
        input_per_mtok: ${ADLC_TEST_PRICE_IN}
policy: {default: deny}
`

func TestPricingFromEnv(t *testing.T) {
	t.Setenv("ADLC_TEST_PRICE_IN", "3.50")
	t.Setenv("ADLC_TEST_PRICE_CR", "0.35")
	a, err := Parse([]byte(pricingEnvYAML))
	if err != nil {
		t.Fatal(err)
	}
	want := model.Price{InputPerMTok: 3_500_000, OutputPerMTok: 15_000_000, CacheReadPerMTok: 350_000}
	if got := a.Price(); got != want {
		t.Errorf("Price() = %+v, want %+v", got, want)
	}
	if got := a.Prices()["f"].InputPerMTok; got != 3_500_000 {
		t.Errorf("fallback input price = %d, want 3500000", got)
	}
}

func TestPricingEnvErrors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"unset", nil, []string{
			"model.pricing.input_per_mtok: environment variable ADLC_TEST_PRICE_IN is not set",
			"model.pricing.cache_read_per_mtok: environment variable ADLC_TEST_PRICE_CR is not set",
			"model.fallbacks[0].pricing.input_per_mtok: environment variable ADLC_TEST_PRICE_IN is not set",
		}},
		{
			"empty counts as unset",
			map[string]string{"ADLC_TEST_PRICE_IN": "", "ADLC_TEST_PRICE_CR": "1"},
			[]string{"model.pricing.input_per_mtok: environment variable ADLC_TEST_PRICE_IN is not set"},
		},
		{
			"expanded value is validated",
			map[string]string{"ADLC_TEST_PRICE_IN": "abc", "ADLC_TEST_PRICE_CR": "1"},
			[]string{`model.pricing.input_per_mtok: "abc" is not a decimal amount`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Parse([]byte(pricingEnvYAML))
			for _, w := range tt.want {
				wantErr(t, err, w)
			}
		})
	}
}
