package harness

import (
	"context"
	"fmt"
	"regexp"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// HookDecision is a PreTool hook's answer.
type HookDecision struct {
	Deny   bool
	Reason string // required when Deny is true; empty becomes "denied by hook"
}

// Hook observes and may block tool calls. Hooks run in Config.Hooks order.
type Hook interface {
	// Name identifies the hook in events, e.g. "pii-guard". It must match
	// ^[a-z0-9-]{1,40}$ and be unique within Config.Hooks.
	Name() string
	// PreTool runs after tool_call_requested and before the policy check.
	PreTool(ctx context.Context, call model.ToolCall, tool toolrouter.Tool) (HookDecision, error)
	// PostTool runs after tool_call_completed. It cannot change the result.
	PostTool(ctx context.Context, call model.ToolCall, res toolrouter.Result) error
}

var hookNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// validateHooks checks hook names: valid and unique.
func validateHooks(hooks []Hook) error {
	seen := make(map[string]bool, len(hooks))
	for _, h := range hooks {
		name := h.Name()
		if !hookNamePattern.MatchString(name) {
			return fmt.Errorf("harness: config: invalid hook name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("harness: config: duplicate hook name %q", name)
		}
		seen[name] = true
	}
	return nil
}

// runPreHooks runs the PreTool hooks in order. It returns true when the call
// must not run (the run has been finished).
func (l *loop) runPreHooks(call model.ToolCall, tool toolrouter.Tool, reqSeq uint64) (bool, error) {
	for _, h := range l.cfg.Hooks {
		d, err := h.PreTool(l.callCtx, call, tool)
		if err != nil {
			return true, l.hookError(h, err)
		}
		if !d.Deny {
			continue
		}
		reason := d.Reason
		if reason == "" {
			reason = "denied by hook"
		}
		ruleID := "hook:" + h.Name()
		if _, err := l.add(&adlcv1.Event{ParentSeq: reqSeq, Payload: &adlcv1.Event_PolicyDecided{PolicyDecided: &adlcv1.PolicyDecided{
			CallId: call.ID, Decision: adlcv1.Decision_DECISION_DENY, RuleId: ruleID, Reason: reason,
			Source: adlcv1.DecisionSource_DECISION_SOURCE_HOOK,
		}}}); err != nil {
			return true, err
		}
		return true, l.finish(adlcv1.StopReason_STOP_REASON_DENIED, "", fmt.Sprintf("rule %s: %s", ruleID, reason))
	}
	return false, nil
}

// hookError ends the run because a PreTool hook failed.
func (l *loop) hookError(h Hook, err error) error {
	if stopped, err2 := l.stopForContext(); stopped {
		return err2
	}
	if err2 := l.recordHookError(h, err); err2 != nil {
		return err2
	}
	return l.finish(adlcv1.StopReason_STOP_REASON_ERROR, "", "hook "+h.Name()+": "+err.Error())
}

func (l *loop) recordHookError(h Hook, err error) error {
	_, err2 := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{
		ErrorClass: adlcv1.ErrorClass_ERROR_CLASS_INTERNAL, Message: err.Error(), Component: "hook:" + h.Name(),
	}}})
	return err2
}

// runPostHooks runs the PostTool hooks in order. A hook error is recorded and
// the run goes on: PostTool hooks only observe.
func (l *loop) runPostHooks(call model.ToolCall, res toolrouter.Result) error {
	for _, h := range l.cfg.Hooks {
		if err := h.PostTool(l.callCtx, call, res); err != nil {
			if err2 := l.recordHookError(h, err); err2 != nil {
				return err2
			}
		}
	}
	return nil
}

// writeCheckpoint records the digest of the state so far. The digest is taken
// before the checkpoint event is added, so it describes the state it follows.
func (l *loop) writeCheckpoint(step uint32) error {
	digest, err := l.r.state.Digest()
	if err != nil {
		return fmt.Errorf("harness: %w", err)
	}
	_, err = l.add(&adlcv1.Event{Payload: &adlcv1.Event_Checkpoint{Checkpoint: &adlcv1.Checkpoint{
		Step: step, StateDigest: digest,
	}}})
	return err
}

// VerifyCheckpoints folds events and checks that every checkpoint's
// state_digest equals the digest of the state just before it. It returns nil
// if all match.
func VerifyCheckpoints(events []*adlcv1.Event) error {
	var s eventlog.RunState
	for _, ev := range events {
		if cp := ev.GetCheckpoint(); cp != nil {
			got, err := s.Digest()
			if err != nil {
				return fmt.Errorf("harness: %w", err)
			}
			if got != cp.GetStateDigest() {
				return fmt.Errorf("harness: checkpoint at seq %d: digest %s, want %s", ev.GetSeq(), cp.GetStateDigest(), got)
			}
		}
		if err := s.Apply(ev); err != nil {
			return fmt.Errorf("harness: checkpoint check: %w", err)
		}
	}
	return nil
}
