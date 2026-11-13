package policy

import (
	"errors"
	"fmt"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// ErrUnknownName is returned by the Parse functions for a name they do not know.
var ErrUnknownName = errors.New("policy: unknown name")

// riskClassNames maps the names used in agent.yaml to risk classes.
var riskClassNames = map[string]adlcv1.RiskClass{
	"read":           adlcv1.RiskClass_RISK_CLASS_READ,
	"write_local":    adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL,
	"write_external": adlcv1.RiskClass_RISK_CLASS_WRITE_EXTERNAL,
	"exec":           adlcv1.RiskClass_RISK_CLASS_EXEC,
	"irreversible":   adlcv1.RiskClass_RISK_CLASS_IRREVERSIBLE,
}

// decisionNames maps the names used in agent.yaml to decisions.
var decisionNames = map[string]adlcv1.Decision{
	"allow":          adlcv1.Decision_DECISION_ALLOW,
	"deny":           adlcv1.Decision_DECISION_DENY,
	"needs_approval": adlcv1.Decision_DECISION_NEEDS_APPROVAL,
}

// ParseRiskClass returns the risk class for an agent.yaml name. Matching is
// exact: case-sensitive, no trimming.
func ParseRiskClass(s string) (adlcv1.RiskClass, error) {
	if r, ok := riskClassNames[s]; ok {
		return r, nil
	}
	return 0, fmt.Errorf("%w: risk class %q (want one of read, write_local, write_external, exec, irreversible)", ErrUnknownName, s)
}

// RiskClassName returns the agent.yaml name for r, or "unspecified".
func RiskClassName(r adlcv1.RiskClass) string {
	for name, v := range riskClassNames {
		if v == r {
			return name
		}
	}
	return "unspecified"
}

// ParseDecision returns the decision for an agent.yaml name. Matching is
// exact: case-sensitive, no trimming.
func ParseDecision(s string) (adlcv1.Decision, error) {
	if d, ok := decisionNames[s]; ok {
		return d, nil
	}
	return 0, fmt.Errorf("%w: decision %q (want one of allow, deny, needs_approval)", ErrUnknownName, s)
}

// DecisionName returns the agent.yaml name for d, or "unspecified".
func DecisionName(d adlcv1.Decision) string {
	for name, v := range decisionNames {
		if v == d {
			return name
		}
	}
	return "unspecified"
}
