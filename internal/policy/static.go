package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// Rule matches on risk class, tool name glob and (optionally) arguments.
// All given conditions must hold. First match wins.
type Rule struct {
	ID       string
	Risk     adlcv1.RiskClass      // UNSPECIFIED = any risk
	Tool     string                // "" = any tool; otherwise a glob (a plain name matches exactly)
	Args     map[string]ArgMatcher // nil = any arguments; every entry must match
	Decision adlcv1.Decision
}

// Static is a Client over a fixed rule list. Build it with NewStatic.
type Static struct {
	Rules    []Rule
	Default  adlcv1.Decision // used when no rule matches; must not be UNSPECIFIED
	compiled []compiledRule  // built by NewStatic, same order as Rules
}

// compiledRule is a Rule with its tool glob and argument matchers compiled.
type compiledRule struct {
	tool *regexp.Regexp // nil = any tool
	args []argCheck     // sorted by name
}

// argCheck is one compiled argument matcher.
type argCheck struct {
	name string
	m    ArgMatcher
	re   *regexp.Regexp // glob or regex, else nil
}

// NewStatic validates the rules and returns a Static holding a copy of rules.
func NewStatic(rules []Rule, def adlcv1.Decision) (*Static, error) {
	if def == adlcv1.Decision_DECISION_UNSPECIFIED {
		return nil, errors.New("policy: default decision is unspecified")
	}
	seen := make(map[string]bool, len(rules))
	compiled := make([]compiledRule, 0, len(rules))
	own := make([]Rule, len(rules))
	for i, r := range rules {
		if err := validateRule(i, r, seen); err != nil {
			return nil, err
		}
		seen[r.ID] = true
		own[i] = copyRule(r)
		c, err := compileRule(own[i])
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, c)
	}
	return &Static{Rules: own, Default: def, compiled: compiled}, nil
}

// copyRule returns r with its own Args map and In slices.
func copyRule(r Rule) Rule {
	if r.Args == nil {
		return r
	}
	args := make(map[string]ArgMatcher, len(r.Args))
	for k, m := range r.Args {
		m.In = slices.Clone(m.In)
		args[k] = m
	}
	r.Args = args
	return r
}

// compileRule compiles the tool glob and argument matchers of r.
func compileRule(r Rule) (compiledRule, error) {
	var c compiledRule
	if r.Tool != "" {
		re, err := globRegexp(r.Tool)
		if err != nil {
			return c, fmt.Errorf("policy: rule %q: tool: %w", r.ID, err)
		}
		c.tool = re
	}
	names := make([]string, 0, len(r.Args))
	for name := range r.Args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" {
			return c, fmt.Errorf("policy: rule %q: args: empty argument name", r.ID)
		}
		m := r.Args[name]
		if err := m.Check(); err != nil {
			return c, fmt.Errorf("policy: rule %q: args.%s: %w", r.ID, name, err)
		}
		ac := argCheck{name: name, m: m}
		switch {
		case m.Glob != "":
			ac.re, _ = globRegexp(m.Glob)
		case m.Regex != "":
			ac.re = regexp.MustCompile("^(?:" + m.Regex + ")$")
		}
		c.args = append(c.args, ac)
	}
	return c, nil
}

// validateRule checks one rule; seen holds the ids of the rules before it.
func validateRule(index int, r Rule, seen map[string]bool) error {
	switch {
	case r.ID == "":
		return fmt.Errorf("policy: rule %d has no id", index)
	case r.ID == "default":
		return errors.New(`policy: rule id "default" is reserved`)
	case seen[r.ID]:
		return fmt.Errorf("policy: duplicate rule id %q", r.ID)
	case r.Decision == adlcv1.Decision_DECISION_UNSPECIFIED:
		return fmt.Errorf("policy: rule %q has no decision", r.ID)
	}
	return nil
}

// Authorize applies the rules to req. The first matching rule decides;
// if none matches, Default decides. Requests with an unspecified risk class
// are denied before any rule is read.
func (s *Static) Authorize(ctx context.Context, req Request) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	if req.Risk == adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED {
		return Decision{
			Decision: adlcv1.Decision_DECISION_DENY,
			RuleID:   "unspecified-risk",
			Reason:   "tool has no risk class",
			Source:   adlcv1.DecisionSource_DECISION_SOURCE_LOCAL,
		}, nil
	}
	args := parseArgs(req.ArgumentsJSON)
	for i, r := range s.Rules {
		if s.matches(i, r, req, args) {
			return Decision{
				Decision: r.Decision,
				RuleID:   r.ID,
				Reason:   "matched rule " + r.ID,
				Source:   adlcv1.DecisionSource_DECISION_SOURCE_LOCAL,
			}, nil
		}
	}
	return Decision{
		Decision: s.Default,
		RuleID:   "default",
		Reason:   "no rule matched",
		Source:   adlcv1.DecisionSource_DECISION_SOURCE_LOCAL,
	}, nil
}

// parseArgs decodes the top-level fields of a JSON object; anything else gives nil.
func parseArgs(argsJSON string) map[string]json.RawMessage {
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil
	}
	return args
}

// matches reports whether rule i (r) applies to req.
func (s *Static) matches(i int, r Rule, req Request, args map[string]json.RawMessage) bool {
	if r.Risk != adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED && r.Risk != req.Risk {
		return false
	}
	c := s.compiled[i]
	if c.tool != nil && !c.tool.MatchString(req.Tool) {
		return false
	}
	for _, ac := range c.args {
		if !ac.matches(args) {
			return false
		}
	}
	return true
}

// matches reports whether the argument is a JSON string the matcher accepts.
func (ac argCheck) matches(args map[string]json.RawMessage) bool {
	raw, ok := args[ac.name]
	raw = bytes.TrimSpace(raw)
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return false
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	switch {
	case ac.re != nil:
		return ac.re.MatchString(v)
	case ac.m.Prefix != "":
		return strings.HasPrefix(v, ac.m.Prefix)
	default:
		return slices.Contains(ac.m.In, v)
	}
}
