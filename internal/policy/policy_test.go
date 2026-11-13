package policy

import (
	"context"
	"errors"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

func TestAllowAll(t *testing.T) {
	d, err := AllowAll{}.Authorize(context.Background(), Request{Tool: "fs__write_file"})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if d.Decision != adlcv1.Decision_DECISION_ALLOW {
		t.Errorf("Decision = %v, want ALLOW", d.Decision)
	}
	if d.RuleID != "allow-all" {
		t.Errorf("RuleID = %q, want allow-all", d.RuleID)
	}
	if d.Source != adlcv1.DecisionSource_DECISION_SOURCE_LOCAL {
		t.Errorf("Source = %v, want LOCAL", d.Source)
	}
}

func TestAllowAllCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, err := AllowAll{}.Authorize(ctx, Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d != (Decision{}) {
		t.Errorf("Decision = %+v, want zero value", d)
	}
}
