package harness

import (
	"context"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// KillSwitch says whether a run has been killed; *controlplane.Store implements it.
type KillSwitch interface {
	// Killed reports whether the run runID of agent agentID (Config.AgentID,
	// "name@version") must stop, and why.
	Killed(ctx context.Context, runID, agentID string) (killed bool, reason string, err error)
}

// killComponent is error_occurred.component for a failed kill check.
const killComponent = "killswitch"

// checkKill asks the kill switch whether the run must stop. If it must,
// checkKill finishes the run (CANCELLED, "killed: <reason>") and returns true.
// If the check fails, it records error_occurred and returns false: the run goes on.
func (l *loop) checkKill() (bool, error) {
	if l.deps.KillSwitch == nil {
		return false, nil
	}
	killed, reason, err := l.deps.KillSwitch.Killed(l.callCtx, l.cfg.RunID, l.cfg.AgentID)
	if err != nil {
		if stopped, err2 := l.stopForContext(); stopped {
			return true, err2
		}
		_, err2 := l.add(&adlcv1.Event{Payload: &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{
			ErrorClass: adlcv1.ErrorClass_ERROR_CLASS_INTERNAL, Message: err.Error(), Component: killComponent,
		}}})
		return false, err2
	}
	if !killed {
		return false, nil
	}
	return true, l.finish(adlcv1.StopReason_STOP_REASON_CANCELLED, "", "killed: "+reason)
}
