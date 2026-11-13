package agentconfig

import (
	"fmt"
	"path/filepath"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/contextmgr"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
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

// ParsedHook is a Hook with parsed durations. Zero durations mean the
// plugin's defaults.
type ParsedHook struct {
	Name         string
	Command      []string
	Env          map[string]string
	Dir          string // = Agent.Dir
	StartTimeout time.Duration
	CallTimeout  time.Duration
}

// HookPlugins returns the declared hooks in file order.
// Call only after Validate succeeded.
func (a *Agent) HookPlugins() []ParsedHook {
	out := make([]ParsedHook, 0, len(a.Hooks))
	for _, h := range a.Hooks {
		ph := ParsedHook{Name: h.Name, Command: append([]string(nil), h.Command...), Env: h.Env, Dir: a.Dir}
		ph.StartTimeout, _ = time.ParseDuration(h.StartTimeout)
		ph.CallTimeout, _ = time.ParseDuration(h.CallTimeout)
		out = append(out, ph)
	}
	return out
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

// SandboxWorkspace returns the sandbox workspace directory, made absolute
// against the agent file's directory ("" if there is no sandbox section or
// no workspace).
func (a *Agent) SandboxWorkspace() string {
	if a.Sandbox == nil || a.Sandbox.Workspace == "" {
		return ""
	}
	w := a.Sandbox.Workspace
	if !filepath.IsAbs(w) {
		w = filepath.Join(a.Dir, w)
	}
	return w
}

// SandboxSpec returns the spec of the sandbox for run runID, and false if the
// agent has no sandbox section. Call only after Validate succeeded.
func (a *Agent) SandboxSpec(runID string) (sandbox.Spec, bool) {
	if a.Sandbox == nil {
		return sandbox.Spec{}, false
	}
	return sandbox.Spec{
		RunID:        runID,
		Image:        a.Sandbox.Image,
		WorkspaceDir: a.SandboxWorkspace(),
		CPUs:         a.Sandbox.CPUs,
		MemoryBytes:  a.Sandbox.MemoryMB << 20,
		Network:      a.Sandbox.Network,
	}, true
}
