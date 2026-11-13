package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

const (
	allow   = adlcv1.Decision_DECISION_ALLOW
	deny    = adlcv1.Decision_DECISION_DENY
	ask     = adlcv1.Decision_DECISION_NEEDS_APPROVAL
	read    = adlcv1.RiskClass_RISK_CLASS_READ
	write   = adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL
	exec    = adlcv1.RiskClass_RISK_CLASS_EXEC
	anyRisk = adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED
)

func mustStatic(t *testing.T, rules []Rule, def adlcv1.Decision) *Static {
	t.Helper()
	s, err := NewStatic(rules, def)
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}
	return s
}

func TestNewStaticValidation(t *testing.T) {
	tests := []struct {
		name    string
		rules   []Rule
		def     adlcv1.Decision
		wantErr string // "" means no error
	}{
		{
			name:    "default unspecified",
			rules:   []Rule{{ID: "a", Risk: read, Decision: allow}},
			def:     adlcv1.Decision_DECISION_UNSPECIFIED,
			wantErr: "policy: default decision is unspecified",
		},
		{
			name:    "empty rule id",
			rules:   []Rule{{ID: "", Risk: read, Decision: allow}},
			def:     deny,
			wantErr: "policy: rule 0 has no id",
		},
		{
			name: "duplicate rule id",
			rules: []Rule{
				{ID: "a", Risk: read, Decision: allow},
				{ID: "a", Risk: write, Decision: deny},
			},
			def:     deny,
			wantErr: `policy: duplicate rule id "a"`,
		},
		{
			name:    "rule decision unspecified",
			rules:   []Rule{{ID: "a", Risk: read}},
			def:     deny,
			wantErr: `policy: rule "a" has no decision`,
		},
		{
			name:    "reserved default id",
			rules:   []Rule{{ID: "default", Risk: read, Decision: allow}},
			def:     deny,
			wantErr: `policy: rule id "default" is reserved`,
		},
		{
			name: "valid",
			rules: []Rule{
				{ID: "a", Risk: read, Decision: allow},
				{ID: "b", Tool: "fs__write_file", Decision: ask},
			},
			def: deny,
		},
		{
			name:  "valid with no rules",
			rules: nil,
			def:   deny,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewStatic(tt.rules, tt.def)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewStaticCopiesRules(t *testing.T) {
	rules := []Rule{{ID: "a", Risk: read, Decision: allow}}
	s := mustStatic(t, rules, deny)
	rules[0].Decision = deny
	rules[0].ID = "changed"

	d, err := s.Authorize(context.Background(), Request{Risk: read})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if d.Decision != allow || d.RuleID != "a" {
		t.Errorf("got %v by %q, want ALLOW by a", d.Decision, d.RuleID)
	}
}

func TestStaticFirstMatchWins(t *testing.T) {
	s := mustStatic(t, []Rule{
		{ID: "a", Risk: read, Decision: allow},
		{ID: "b", Risk: read, Decision: deny},
	}, deny)
	d, err := s.Authorize(context.Background(), Request{Tool: "fs__read_file", Risk: read})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if d.Decision != allow || d.RuleID != "a" {
		t.Errorf("got %v by %q, want ALLOW by a", d.Decision, d.RuleID)
	}
	if d.Reason != "matched rule a" || d.Source != adlcv1.DecisionSource_DECISION_SOURCE_LOCAL {
		t.Errorf("Reason = %q, Source = %v", d.Reason, d.Source)
	}
}

func TestStaticMatchesTool(t *testing.T) {
	s := mustStatic(t, []Rule{
		{ID: "w", Risk: anyRisk, Tool: "fs__write_file", Decision: ask},
	}, allow)

	tests := []struct {
		name    string
		req     Request
		wantID  string
		wantDec adlcv1.Decision
	}{
		{"write tool, local risk", Request{Tool: "fs__write_file", Risk: write}, "w", ask},
		{"write tool, exec risk", Request{Tool: "fs__write_file", Risk: exec}, "w", ask},
		{"other tool falls through", Request{Tool: "fs__read_file", Risk: read}, "default", allow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := s.Authorize(context.Background(), tt.req)
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if d.RuleID != tt.wantID || d.Decision != tt.wantDec {
				t.Errorf("got %v by %q, want %v by %q", d.Decision, d.RuleID, tt.wantDec, tt.wantID)
			}
		})
	}
}

func TestStaticRiskAndToolBothMustMatch(t *testing.T) {
	s := mustStatic(t, []Rule{
		{ID: "x", Risk: exec, Tool: "shell__run", Decision: allow},
	}, deny)
	d, err := s.Authorize(context.Background(), Request{Tool: "shell__run", Risk: read})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if d.RuleID != "default" || d.Decision != deny {
		t.Errorf("got %v by %q, want default DENY", d.Decision, d.RuleID)
	}
}

func TestStaticDefault(t *testing.T) {
	s := mustStatic(t, []Rule{{ID: "a", Risk: exec, Decision: allow}}, ask)
	d, err := s.Authorize(context.Background(), Request{Tool: "fs__read_file", Risk: read})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if d.Decision != ask || d.RuleID != "default" || d.Reason != "no rule matched" {
		t.Errorf("got %+v, want NEEDS_APPROVAL by default, reason no rule matched", d)
	}
}

func TestStaticUnspecifiedRiskDenied(t *testing.T) {
	// The rule would allow it, but an unspecified risk is denied before rules are read.
	s := mustStatic(t, []Rule{{ID: "any", Risk: anyRisk, Decision: allow}}, allow)
	d, err := s.Authorize(context.Background(), Request{Tool: "fs__read_file"})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if d.Decision != deny || d.RuleID != "unspecified-risk" {
		t.Errorf("got %v by %q, want DENY by unspecified-risk", d.Decision, d.RuleID)
	}
}

func TestStaticCancelled(t *testing.T) {
	s := mustStatic(t, nil, allow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, err := s.Authorize(ctx, Request{Risk: read})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d != (Decision{}) {
		t.Errorf("Decision = %+v, want zero value", d)
	}
}
