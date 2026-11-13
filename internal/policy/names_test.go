package policy

import (
	"errors"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

func TestParseRiskClass(t *testing.T) {
	names := map[string]adlcv1.RiskClass{
		"read":           adlcv1.RiskClass_RISK_CLASS_READ,
		"write_local":    adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL,
		"write_external": adlcv1.RiskClass_RISK_CLASS_WRITE_EXTERNAL,
		"exec":           adlcv1.RiskClass_RISK_CLASS_EXEC,
		"irreversible":   adlcv1.RiskClass_RISK_CLASS_IRREVERSIBLE,
	}
	for name, want := range names {
		t.Run("round-trip/"+name, func(t *testing.T) {
			got, err := ParseRiskClass(name)
			if err != nil {
				t.Fatalf("ParseRiskClass(%q): %v", name, err)
			}
			if got != want {
				t.Errorf("ParseRiskClass(%q) = %v, want %v", name, got, want)
			}
			if back := RiskClassName(got); back != name {
				t.Errorf("RiskClassName(%v) = %q, want %q", got, back, name)
			}
		})
	}
	for _, bad := range []string{"Read", " read", "", "READ", "read "} {
		t.Run("unknown/"+bad, func(t *testing.T) {
			_, err := ParseRiskClass(bad)
			if !errors.Is(err, ErrUnknownName) {
				t.Errorf("ParseRiskClass(%q) err = %v, want ErrUnknownName", bad, err)
			}
		})
	}
}

func TestParseDecision(t *testing.T) {
	names := map[string]adlcv1.Decision{
		"allow":          adlcv1.Decision_DECISION_ALLOW,
		"deny":           adlcv1.Decision_DECISION_DENY,
		"needs_approval": adlcv1.Decision_DECISION_NEEDS_APPROVAL,
	}
	for name, want := range names {
		t.Run("round-trip/"+name, func(t *testing.T) {
			got, err := ParseDecision(name)
			if err != nil {
				t.Fatalf("ParseDecision(%q): %v", name, err)
			}
			if got != want {
				t.Errorf("ParseDecision(%q) = %v, want %v", name, got, want)
			}
			if back := DecisionName(got); back != name {
				t.Errorf("DecisionName(%v) = %q, want %q", got, back, name)
			}
		})
	}
	for _, bad := range []string{"Allow", " deny", ""} {
		t.Run("unknown/"+bad, func(t *testing.T) {
			_, err := ParseDecision(bad)
			if !errors.Is(err, ErrUnknownName) {
				t.Errorf("ParseDecision(%q) err = %v, want ErrUnknownName", bad, err)
			}
		})
	}
}

func TestNamesUnspecified(t *testing.T) {
	if got := RiskClassName(0); got != "unspecified" {
		t.Errorf("RiskClassName(0) = %q, want unspecified", got)
	}
	if got := DecisionName(99); got != "unspecified" {
		t.Errorf("DecisionName(99) = %q, want unspecified", got)
	}
}
