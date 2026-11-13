// Package policy decides whether a tool call may run. A Client answers one
// question per call: allow, deny, or ask a human. The static engine in this
// package decides from a fixed, ordered rule list read from agent.yaml.
package policy

import (
	"context"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// Request is what is being authorized.
type Request struct {
	AgentID       string
	Tool          string // namespaced tool name
	Risk          adlcv1.RiskClass
	ArgumentsJSON string
}

// Decision is the answer.
type Decision struct {
	Decision adlcv1.Decision // ALLOW, DENY, NEEDS_APPROVAL
	RuleID   string          // which rule decided; "default" if none matched
	Reason   string
	Source   adlcv1.DecisionSource // LOCAL for the static engine
}

// Client decides whether a tool call may run.
type Client interface {
	Authorize(ctx context.Context, req Request) (Decision, error)
}

// AllowAll allows everything. Tests only.
type AllowAll struct{}

// Authorize allows every request, or returns the context error if ctx is done.
func (AllowAll) Authorize(ctx context.Context, _ Request) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	return Decision{
		Decision: adlcv1.Decision_DECISION_ALLOW,
		RuleID:   "allow-all",
		Reason:   "all calls allowed",
		Source:   adlcv1.DecisionSource_DECISION_SOURCE_LOCAL,
	}, nil
}

var (
	_ Client = AllowAll{}
	_ Client = (*Static)(nil)
)
