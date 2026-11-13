package agentconfig

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/policy"
)

type problems []error

func (p *problems) add(path, format string, args ...any) {
	*p = append(*p, fmt.Errorf("%s: "+format, append([]any{path}, args...)...))
}

var (
	nameRE   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	serverRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
)

const moneyMsg = "%q is not a decimal amount with at most 6 decimals"

// Validate checks every field and returns all problems joined with
// errors.Join. Each problem is an error whose text is "<path>: <message>".
func (a *Agent) Validate() error {
	var p problems
	if !nameRE.MatchString(a.Name) {
		p.add("name", "must match ^[a-z][a-z0-9-]{0,31}$, got %q", a.Name)
	}
	if a.Version < 1 {
		p.add("version", "must be >= 1")
	}
	a.validateModel(&p)
	if a.MaxToolOutputBytes < 1 || a.MaxToolOutputBytes > 10485760 {
		p.add("max_tool_output_bytes", "must be between 1 and 10485760")
	}
	a.validateTools(&p)
	a.validateBudget(&p)
	a.validateContext(&p)
	a.validatePolicy(&p)
	return errors.Join(p...)
}

func (a *Agent) validateModel(p *problems) {
	validateModelFields(p, "model", a.Model)
	for i, f := range a.Model.Fallbacks {
		path := fmt.Sprintf("model.fallbacks[%d]", i)
		validateModelFields(p, path, f)
		if len(f.Fallbacks) > 0 {
			p.add(path+".fallbacks", "not allowed")
		}
		if f.Provider == a.Model.Provider && f.Name == a.Model.Name {
			p.add(path, "repeats the primary provider and name")
		}
	}
}

// validateModelFields checks one model; path is "model" or "model.fallbacks[i]".
func validateModelFields(p *problems, path string, m Model) {
	switch m.Provider {
	case "fake", "anthropic", "openai":
	default:
		p.add(path+".provider", "must be one of fake, anthropic, openai, got %q", m.Provider)
	}
	if m.Name == "" {
		p.add(path+".name", "is required")
	}
	if m.MaxTokens < 1 || m.MaxTokens > 1000000 {
		p.add(path+".max_tokens", "must be between 1 and 1000000")
	}
	switch m.Effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		p.add(path+".effort", "must be one of low, medium, high, xhigh, max, got %q", m.Effort)
	}
	fields := []struct{ name, val string }{
		{"input_per_mtok", m.Pricing.InputPerMTok},
		{"output_per_mtok", m.Pricing.OutputPerMTok},
		{"cache_read_per_mtok", m.Pricing.CacheReadPerMTok},
		{"cache_write_per_mtok", m.Pricing.CacheWritePerMTok},
	}
	for _, f := range fields {
		if _, err := ParseMicroUSD(f.val); err != nil {
			p.add(path+".pricing."+f.name, moneyMsg, f.val)
		}
	}
}

func (a *Agent) validateTools(p *problems) {
	seen := map[string]bool{}
	for i, t := range a.Tools {
		path := fmt.Sprintf("tools[%d]", i)
		if !serverRE.MatchString(t.Server) || strings.Contains(t.Server, "__") {
			p.add(path+".server", `must match ^[a-z][a-z0-9_]{0,15}$ without "__", got %q`, t.Server)
		} else if seen[t.Server] {
			p.add(path+".server", "duplicate server %q", t.Server)
		}
		seen[t.Server] = true
		if len(t.Command) == 0 || t.Command[0] == "" {
			p.add(path+".command", "is required")
		}
		names := make([]string, 0, len(t.Risk))
		for k := range t.Risk {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			if _, err := policy.ParseRiskClass(t.Risk[k]); err != nil {
				p.add(path+".risk."+k, "unknown risk class %q", t.Risk[k])
			}
		}
		if t.DefaultRisk != "" {
			if _, err := policy.ParseRiskClass(t.DefaultRisk); err != nil {
				p.add(path+".default_risk", "unknown risk class %q", t.DefaultRisk)
			}
		}
	}
}

func (a *Agent) validateBudget(p *problems) {
	if s := a.Budget.MaxWallClock; s != "" {
		if d, err := time.ParseDuration(s); err != nil || d <= 0 {
			p.add("budget.max_wall_clock", "%q is not a positive duration", s)
		}
	}
	if _, err := ParseMicroUSD(a.Budget.MaxUSD); err != nil {
		p.add("budget.max_usd", moneyMsg, a.Budget.MaxUSD)
	}
}

func (a *Agent) validateContext(p *problems) {
	if a.Context.MaxInputTokens < 0 {
		p.add("context.max_input_tokens", "must be >= 0, got %d", a.Context.MaxInputTokens)
	}
	if a.Context.KeepRecentSteps < 0 {
		p.add("context.keep_recent_steps", "must be >= 0, got %d", a.Context.KeepRecentSteps)
	}
}

func (a *Agent) validatePolicy(p *problems) {
	if _, err := policy.ParseDecision(a.Policy.Default); err != nil {
		p.add("policy.default", "must be one of allow, deny, needs_approval, got %q", a.Policy.Default)
	}
	seen := map[string]bool{}
	for i, r := range a.Policy.Rules {
		path := fmt.Sprintf("policy.rules[%d]", i)
		switch {
		case r.ID == "":
			p.add(path+".id", "is required")
		case r.ID == "default":
			p.add(path+".id", `rule id "default" is reserved`)
		case seen[r.ID]:
			p.add(path+".id", "duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		if r.Risk != "" {
			if _, err := policy.ParseRiskClass(r.Risk); err != nil {
				p.add(path+".risk", "unknown risk class %q", r.Risk)
			}
		}
		if _, err := policy.ParseDecision(r.Decision); err != nil {
			p.add(path+".decision", "must be one of allow, deny, needs_approval, got %q", r.Decision)
		}
	}
}
