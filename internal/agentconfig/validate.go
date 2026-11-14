package agentconfig

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
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
	a.validateSandbox(&p)
	a.validateBudget(&p)
	a.validateContext(&p)
	a.validatePlanning(&p)
	a.validatePolicy(&p)
	a.validateHooks(&p)
	return errors.Join(p...)
}

func (a *Agent) validateModel(p *problems) {
	validateModelFields(p, "model", a.Model, primaryModel)
	for i, f := range a.Model.Fallbacks {
		path := fmt.Sprintf("model.fallbacks[%d]", i)
		validateModelFields(p, path, f, fallbackModel)
		if len(f.Fallbacks) > 0 {
			p.add(path+".fallbacks", "not allowed")
		}
		if f.Roles.Plan != nil {
			p.add(path+".roles", "not allowed")
		}
		if f.Provider == a.Model.Provider && f.Name == a.Model.Name {
			p.add(path, "repeats the primary provider and name")
		}
	}
	if r := a.Model.Roles.Plan; r != nil {
		validateModelFields(p, "model.roles.plan", *r, roleModel)
		if len(r.Fallbacks) > 0 {
			p.add("model.roles.plan.fallbacks", "not allowed")
		}
		if r.Roles.Plan != nil {
			p.add("model.roles.plan.roles", "not allowed")
		}
	}
}

// modelKind says where a Model appears in the agent file.
type modelKind int

const (
	primaryModel modelKind = iota
	fallbackModel
	roleModel
)

// validateModelFields checks one model; path is "model", "model.fallbacks[i]"
// or "model.roles.plan".
func validateModelFields(p *problems, path string, m Model, kind modelKind) {
	switch m.Provider {
	case "fake", "anthropic", "openai":
	default:
		p.add(path+".provider", "must be one of fake, anthropic, openai, got %q", m.Provider)
	}
	if m.Name == "" {
		p.add(path+".name", "is required")
	}
	switch kind {
	case primaryModel:
		validateTuning(p, path, m)
	case fallbackModel:
		validateFallbackTuning(p, path, m)
	case roleModel:
		validateRoleTuning(p, path, m)
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

// validateTuning checks max_tokens and effort of the primary model.
func validateTuning(p *problems, path string, m Model) {
	if m.MaxTokens < 1 || m.MaxTokens > 1000000 {
		p.add(path+".max_tokens", "must be between 1 and 1000000")
	}
	validateEffort(p, path+".effort", m.Effort)
}

// validateEffort checks an effort level; "" is allowed.
func validateEffort(p *problems, path, effort string) {
	switch effort {
	case "", "low", "medium", "high", "xhigh", "max":
	default:
		p.add(path, "must be one of low, medium, high, xhigh, max, got %q", effort)
	}
}

// validateRoleTuning rejects max_tokens on a role (the plan call uses
// planning.max_tokens) and checks its effort.
func validateRoleTuning(p *problems, path string, m Model) {
	if m.MaxTokens != 0 {
		p.add(path+".max_tokens", "not supported on roles (planning.max_tokens is used)")
	}
	validateEffort(p, path+".effort", m.Effort)
}

// validateFallbackTuning rejects max_tokens and effort on a fallback: the
// harness sends the primary's values and the router passes them through.
func validateFallbackTuning(p *problems, path string, m Model) {
	if m.MaxTokens != 0 {
		p.add(path+".max_tokens", "not supported on fallbacks (the primary's value is used)")
	}
	if m.Effort != "" {
		p.add(path+".effort", "not supported on fallbacks (the primary's value is used)")
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

func (a *Agent) validatePlanning(p *problems) {
	if v := a.Planning.MaxTokens; v < 0 || v > 1000000 {
		p.add("planning.max_tokens", "must be between 0 and 1000000, got %d", v)
	}
}

func (a *Agent) validatePolicy(p *problems) {
	if _, err := policy.ParseDecision(a.Policy.Default); err != nil {
		p.add("policy.default", "must be one of allow, deny, needs_approval, got %q", a.Policy.Default)
	}
	seen := map[string]bool{}
	for _, ir := range a.Policy.Included {
		validateRule(p, fmt.Sprintf("%s: rules[%d]", ir.File, ir.Index), ir.Rule, seen)
	}
	for i, r := range a.Policy.Rules {
		validateRule(p, fmt.Sprintf("policy.rules[%d]", i), r, seen)
	}
}

// validateRule checks one rule; seen holds the ids of the rules before it.
func validateRule(p *problems, path string, r Rule, seen map[string]bool) {
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
	if r.Tool != "" {
		if err := policy.CheckGlob(r.Tool); err != nil {
			p.add(path+".tool", "%v", err)
		}
	}
	validateArgs(p, path, r.Args)
}

// validateArgs checks the argument matchers of a rule in name order.
func validateArgs(p *problems, path string, args map[string]ArgMatcher) {
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" {
			p.add(path+".args", "empty argument name")
			continue
		}
		if err := args[name].toPolicy().Check(); err != nil {
			p.add(path+".args."+name, "%v", err)
		}
	}
}

var hookNameRE = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

func (a *Agent) validateHooks(p *problems) {
	seen := map[string]bool{}
	for i, h := range a.Hooks {
		path := fmt.Sprintf("hooks[%d]", i)
		switch {
		case !hookNameRE.MatchString(h.Name):
			p.add(path+".name", "must match ^[a-z0-9-]{1,40}$, got %q", h.Name)
		case seen[h.Name]:
			p.add(path+".name", "duplicate hook name %q", h.Name)
		}
		seen[h.Name] = true
		if len(h.Command) == 0 || h.Command[0] == "" {
			p.add(path+".command", "is required")
		}
		for _, d := range []struct{ field, val string }{
			{"start_timeout", h.StartTimeout}, {"call_timeout", h.CallTimeout},
		} {
			if d.val == "" {
				continue
			}
			if v, err := time.ParseDuration(d.val); err != nil || v <= 0 {
				p.add(path+"."+d.field, "%q is not a positive duration", d.val)
			}
		}
	}
}

// validateSandbox checks the sandbox section and that {{sandbox_id}} is only
// used when the section exists.
func (a *Agent) validateSandbox(p *problems) {
	if sb := a.Sandbox; sb != nil {
		validateSandboxFields(p, a.SandboxWorkspace(), *sb)
		return
	}
	for i, t := range a.Tools {
		for _, arg := range t.Command {
			if strings.Contains(arg, sandbox.SandboxIDPlaceholder) {
				p.add(fmt.Sprintf("tools[%d].command", i),
					"uses %s but the agent has no sandbox section", sandbox.SandboxIDPlaceholder)
				break
			}
		}
	}
}

func validateSandboxFields(p *problems, workspace string, sb Sandbox) {
	if sb.Workspace == "" {
		p.add("sandbox.workspace", "is required")
	} else if fi, err := os.Stat(workspace); err != nil || !fi.IsDir() {
		p.add("sandbox.workspace", "%q is not an existing directory", workspace)
	}
	if sb.CPUs < 0 {
		p.add("sandbox.cpus", "must be >= 0, got %v", sb.CPUs)
	}
	if sb.MemoryMB < 0 {
		p.add("sandbox.memory_mb", "must be >= 0, got %d", sb.MemoryMB)
	}
}
