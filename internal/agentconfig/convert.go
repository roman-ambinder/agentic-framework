package agentconfig

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/contextmgr"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
)

// ParsedToolServer is a ToolServer with parsed risk classes.
type ParsedToolServer struct {
	Name        string
	Command     []string
	Env         map[string]string
	Dir         string // = Agent.Dir (commands run in the agent file's directory)
	Risk        map[string]adlcv1.RiskClass
	DefaultRisk adlcv1.RiskClass
}

// AgentID returns "<name>@<version>".
func (a *Agent) AgentID() string { return fmt.Sprintf("%s@%d", a.Name, a.Version) }

// Price returns the model prices in micro-USD per million tokens.
// Call only after Validate succeeded.
func (a *Agent) Price() model.Price {
	return priceOf(a.Model.Pricing)
}

// Prices returns the price of the primary model and of every fallback, keyed
// by model name. If two candidates share a name, the primary's price wins.
// Call only after Validate succeeded.
func (a *Agent) Prices() map[string]model.Price {
	out := map[string]model.Price{a.Model.Name: a.Price()}
	for _, f := range a.Model.Fallbacks {
		if _, ok := out[f.Name]; !ok {
			out[f.Name] = priceOf(f.Pricing)
		}
	}
	return out
}

// priceOf converts decimal USD per million tokens to micro-USD.
func priceOf(p Pricing) model.Price {
	in, _ := ParseMicroUSD(p.InputPerMTok)
	out, _ := ParseMicroUSD(p.OutputPerMTok)
	cr, _ := ParseMicroUSD(p.CacheReadPerMTok)
	cw, _ := ParseMicroUSD(p.CacheWritePerMTok)
	return model.Price{InputPerMTok: in, OutputPerMTok: out, CacheReadPerMTok: cr, CacheWritePerMTok: cw}
}

// BudgetProto returns the budget, or nil if every budget field is zero or empty.
// Call only after Validate succeeded.
func (a *Agent) BudgetProto() *adlcv1.Budget {
	b := a.Budget
	if b.MaxSteps == 0 && b.MaxTokens == 0 && b.MaxWallClock == "" && b.MaxUSD == "" && b.MaxToolCalls == 0 {
		return nil
	}
	out := &adlcv1.Budget{MaxSteps: b.MaxSteps, MaxTokens: b.MaxTokens, MaxToolCalls: b.MaxToolCalls}
	if b.MaxWallClock != "" {
		d, _ := time.ParseDuration(b.MaxWallClock)
		out.MaxWallClock = durationpb.New(d)
	}
	out.MaxUsdMicros, _ = ParseMicroUSD(b.MaxUSD)
	return out
}

// ContextBudget returns the context section as a contextmgr.Budget, for
// harness.Config.Context. Zero fields keep their defaults.
func (a *Agent) ContextBudget() contextmgr.Budget {
	return contextmgr.Budget{MaxInputTokens: a.Context.MaxInputTokens, KeepRecentSteps: a.Context.KeepRecentSteps}
}

// PolicyEngine builds the static policy, rules in file order.
// Call only after Validate succeeded.
func (a *Agent) PolicyEngine() *policy.Static {
	rules := make([]policy.Rule, 0, len(a.Policy.Rules))
	for _, r := range a.Policy.Rules {
		var risk adlcv1.RiskClass
		if r.Risk != "" {
			risk, _ = policy.ParseRiskClass(r.Risk)
		}
		d, _ := policy.ParseDecision(r.Decision)
		rules = append(rules, policy.Rule{ID: r.ID, Risk: risk, Tool: r.Tool, Decision: d})
	}
	def, _ := policy.ParseDecision(a.Policy.Default)
	s, _ := policy.NewStatic(rules, def)
	return s
}

// ToolServers returns the tool servers with parsed risk classes.
// Call only after Validate succeeded.
func (a *Agent) ToolServers() []ParsedToolServer {
	out := make([]ParsedToolServer, 0, len(a.Tools))
	for _, t := range a.Tools {
		ps := ParsedToolServer{
			Name:    t.Server,
			Command: append([]string(nil), t.Command...),
			Env:     t.Env,
			Dir:     a.Dir,
			Risk:    make(map[string]adlcv1.RiskClass, len(t.Risk)),
		}
		for k, v := range t.Risk {
			ps.Risk[k], _ = policy.ParseRiskClass(v)
		}
		if t.DefaultRisk != "" {
			ps.DefaultRisk, _ = policy.ParseRiskClass(t.DefaultRisk)
		}
		out = append(out, ps)
	}
	return out
}
