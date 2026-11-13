package policy

import (
	"context"
	"errors"
	"fmt"
	"slices"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// Rule matches on risk class and (optionally) tool name. First match wins.
type Rule struct {
	ID       string
	Risk     adlcv1.RiskClass // UNSPECIFIED = any risk
	Tool     string           // "" = any tool; exact match otherwise
	Decision adlcv1.Decision
}

// Static is a Client over a fixed rule list. Build it with NewStatic.
type Static struct {
	Rules   []Rule
	Default adlcv1.Decision // used when no rule matches; must not be UNSPECIFIED
}

// NewStatic validates the rules and returns a Static holding a copy of rules.
func NewStatic(rules []Rule, def adlcv1.Decision) (*Static, error) {
	if def == adlcv1.Decision_DECISION_UNSPECIFIED {
		return nil, errors.New("policy: default decision is unspecified")
	}
	seen := make(map[string]bool, len(rules))
	for i, r := range rules {
		if err := validateRule(i, r, seen); err != nil {
			return nil, err
		}
		seen[r.ID] = true
	}
	return &Static{Rules: slices.Clone(rules), Default: def}, nil
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
	for _, r := range s.Rules {
		if matches(r, req) {
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

// matches reports whether r applies to req. Tool names compare exactly.
func matches(r Rule, req Request) bool {
	riskOK := r.Risk == adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED || r.Risk == req.Risk
	toolOK := r.Tool == "" || r.Tool == req.Tool
	return riskOK && toolOK
}
