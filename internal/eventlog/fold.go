package eventlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// ErrInvalidLog means a sequence of events breaks the log's rules, so no
// state can be derived from it.
var ErrInvalidLog = errors.New("eventlog: invalid log")

// Status is where a run is in its lifecycle.
type Status int

// Run statuses.
const (
	// StatusRunning: started and not stopped.
	StatusRunning Status = iota + 1
	// StatusPaused: stopped with STOP_REASON_AWAITING_APPROVAL. The run
	// continues in place once an approval_resolved event is appended.
	StatusPaused
	// StatusFinished: stopped for any other reason. No more events allowed.
	StatusFinished
)

func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusPaused:
		return "paused"
	case StatusFinished:
		return "finished"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// Usage is the total cost of a run so far.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	USDMicros        int64 `json:"usd_micros"`
}

func (u *Usage) add(c *adlcv1.Cost) {
	u.InputTokens += c.GetInputTokens()
	u.OutputTokens += c.GetOutputTokens()
	u.CacheReadTokens += c.GetCacheReadTokens()
	u.CacheWriteTokens += c.GetCacheWriteTokens()
	u.USDMicros += c.GetUsdMicros()
}

// CheckpointRef points at the latest checkpoint event.
type CheckpointRef struct {
	Seq         uint64 `json:"seq"`
	Step        uint32 `json:"step"`
	SnapshotRef string `json:"snapshot_ref"`
	StateDigest string `json:"state_digest"`
}

// RunState is what is known about a run after applying its events in order.
// The zero value is ready to Apply the run's first event.
type RunState struct {
	RunID             string            `json:"run_id"`
	AgentID           string            `json:"agent_id"`
	AgentVersion      string            `json:"agent_version"`
	Task              string            `json:"task"`
	WorkflowID        string            `json:"workflow_id"`
	TaskID            string            `json:"task_id"`
	ResumedFromRunID  string            `json:"resumed_from_run_id"`
	Budget            *adlcv1.Budget    `json:"-"` // included in Digest separately
	StartedAt         time.Time         `json:"started_at"`
	LastEventAt       time.Time         `json:"last_event_at"`
	LastSeq           uint64            `json:"last_seq"`
	Status            Status            `json:"status"`
	StopReason        adlcv1.StopReason `json:"stop_reason"`
	ExhaustedBudget   adlcv1.BudgetKind `json:"exhausted_budget"`
	Result            string            `json:"result"`
	StopDetail        string            `json:"stop_detail"`
	Step              uint32            `json:"step"`
	ModelCalls        int               `json:"model_calls"`
	ModelCallFailures int               `json:"model_call_failures"`
	ToolCalls         int               `json:"tool_calls"`
	ToolErrors        int               `json:"tool_errors"`
	Denials           int               `json:"denials"`
	Errors            int               `json:"errors"`
	Usage             Usage             `json:"usage"`
	// Tool calls requested but not yet completed, in request order.
	PendingToolCalls []string `json:"pending_tool_calls"`
	// Approvals requested but not yet resolved, in request order.
	PendingApprovals []string       `json:"pending_approvals"`
	LastCheckpoint   *CheckpointRef `json:"last_checkpoint"`
	// Events with a payload kind this binary does not know (written by a
	// newer version). They are skipped, apart from their seq and cost.
	UnknownEvents int `json:"unknown_events"`
}

// Fold applies events, which must be a whole run from seq 1, to a new state.
func Fold(events []*adlcv1.Event) (*RunState, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: no events", ErrInvalidLog)
	}
	s := &RunState{}
	for _, ev := range events {
		if err := s.Apply(ev); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Apply updates the state with the run's next event. On error the state is
// unchanged, so a caller tailing the log can report the bad event and stop.
func (s *RunState) Apply(ev *adlcv1.Event) error {
	next := *s // work on a copy; commit only on success
	next.PendingToolCalls = slices.Clone(s.PendingToolCalls)
	next.PendingApprovals = slices.Clone(s.PendingApprovals)
	if err := next.apply(ev); err != nil {
		return fmt.Errorf("%w: run %q seq %d: %w", ErrInvalidLog, ev.GetRunId(), ev.GetSeq(), err)
	}
	*s = next
	return nil
}

func (s *RunState) apply(ev *adlcv1.Event) error {
	if ev.GetSeq() != s.LastSeq+1 {
		return fmt.Errorf("expected seq %d", s.LastSeq+1)
	}
	if s.LastSeq == 0 {
		if ev.GetRunStarted() == nil {
			return errors.New("first event must be run_started")
		}
	} else if ev.GetRunId() != s.RunID {
		return fmt.Errorf("event belongs to run %q", ev.GetRunId())
	}
	switch s.Status {
	case StatusFinished:
		return fmt.Errorf("run already finished (%s)", s.StopReason)
	case StatusPaused:
		sandbox := ev.GetSandboxSuspended() != nil || ev.GetSandboxRestored() != nil
		if ev.GetApprovalResolved() == nil && !sandbox {
			return errors.New("paused run can only continue with approval_resolved or a sandbox event")
		}
		if !sandbox {
			s.Status = StatusRunning
		}
	}

	s.LastSeq = ev.GetSeq()
	s.LastEventAt = ev.GetTime().AsTime()
	s.Usage.add(ev.GetCost())

	switch p := ev.GetPayload().(type) {
	case *adlcv1.Event_RunStarted:
		if s.RunID != "" {
			return errors.New("run_started after start")
		}
		r := p.RunStarted
		s.RunID = ev.GetRunId()
		s.AgentID = ev.GetAgentId()
		s.AgentVersion = r.GetAgentVersion()
		s.Task = r.GetTask()
		s.WorkflowID = r.GetWorkflowId()
		s.TaskID = r.GetTaskId()
		s.ResumedFromRunID = r.GetResumedFromRunId()
		s.Budget = r.GetBudget()
		s.StartedAt = ev.GetTime().AsTime()
		s.Status = StatusRunning

	case *adlcv1.Event_RunCompleted:
		r := p.RunCompleted
		if r.GetStopReason() == adlcv1.StopReason_STOP_REASON_UNSPECIFIED {
			return errors.New("run_completed without a stop reason")
		}
		s.StopReason = r.GetStopReason()
		s.ExhaustedBudget = r.GetExhaustedBudget()
		s.Result = r.GetResult()
		s.StopDetail = r.GetDetail()
		if s.StopReason == adlcv1.StopReason_STOP_REASON_AWAITING_APPROVAL {
			if len(s.PendingApprovals) == 0 {
				return errors.New("awaiting approval with no pending approval")
			}
			s.Status = StatusPaused
		} else {
			s.Status = StatusFinished
		}

	case *adlcv1.Event_ModelCallStarted:
		s.ModelCalls++
		s.Step = max(s.Step, p.ModelCallStarted.GetStep())

	case *adlcv1.Event_ModelCallCompleted:
		// Content is replayed from the log by the harness; nothing to track.

	case *adlcv1.Event_ModelCallFailed:
		s.ModelCallFailures++

	case *adlcv1.Event_ToolCallRequested:
		id := p.ToolCallRequested.GetCall().GetId()
		if id == "" {
			return errors.New("tool_call_requested without a call id")
		}
		if slices.Contains(s.PendingToolCalls, id) {
			return fmt.Errorf("tool call %q requested twice", id)
		}
		s.ToolCalls++
		s.PendingToolCalls = append(s.PendingToolCalls, id)

	case *adlcv1.Event_ToolCallCompleted:
		id := p.ToolCallCompleted.GetCallId()
		var ok bool
		if s.PendingToolCalls, ok = remove(s.PendingToolCalls, id); !ok {
			return fmt.Errorf("tool call %q completed but not pending", id)
		}
		if p.ToolCallCompleted.GetIsError() {
			s.ToolErrors++
		}

	case *adlcv1.Event_PolicyDecided:
		if p.PolicyDecided.GetDecision() == adlcv1.Decision_DECISION_DENY {
			// A denied call never runs, so it is no longer pending.
			s.Denials++
			s.PendingToolCalls, _ = remove(s.PendingToolCalls, p.PolicyDecided.GetCallId())
		}

	case *adlcv1.Event_ApprovalRequested:
		id := p.ApprovalRequested.GetApprovalId()
		if id == "" {
			return errors.New("approval_requested without an approval id")
		}
		if slices.Contains(s.PendingApprovals, id) {
			return fmt.Errorf("approval %q requested twice", id)
		}
		s.PendingApprovals = append(s.PendingApprovals, id)

	case *adlcv1.Event_ApprovalResolved:
		id := p.ApprovalResolved.GetApprovalId()
		var ok bool
		if s.PendingApprovals, ok = remove(s.PendingApprovals, id); !ok {
			return fmt.Errorf("approval %q resolved but not pending", id)
		}
		if !p.ApprovalResolved.GetApproved() {
			s.Denials++
		}

	case *adlcv1.Event_Checkpoint:
		c := p.Checkpoint
		s.LastCheckpoint = &CheckpointRef{
			Seq: ev.GetSeq(), Step: c.GetStep(),
			SnapshotRef: c.GetSnapshotRef(), StateDigest: c.GetStateDigest(),
		}

	case *adlcv1.Event_ErrorOccurred:
		s.Errors++

	case *adlcv1.Event_RunResumed:
		// Changes nothing: a new field would change every digest. The checks
		// above already allow it only while the run is running.

	case *adlcv1.Event_SandboxSuspended, *adlcv1.Event_SandboxRestored:
		// Changes nothing: a new field would change every digest. A paused run
		// stays paused; the checks above allow these only while the run is
		// paused or running.

	case nil:
		s.UnknownEvents++

	default:
		// A kind added to the schema but not handled here. Treat it like an
		// unknown kind; contract tests make sure this is noticed.
		s.UnknownEvents++
	}
	return nil
}

func remove(ids []string, id string) ([]string, bool) {
	i := slices.Index(ids, id)
	if i < 0 {
		return ids, false
	}
	return slices.Delete(ids, i, i+1), true
}

// Digest returns a stable hash of the state, "sha256:<hex>". Two states
// built from the same events always have the same digest; it is stored in
// checkpoints so a resumed run can detect that its log diverged.
func (s *RunState) Digest() (string, error) {
	budget, err := proto.MarshalOptions{Deterministic: true}.Marshal(s.Budget)
	if err != nil {
		return "", fmt.Errorf("eventlog: digest budget: %w", err)
	}
	b, err := json.Marshal(struct {
		*RunState
		Budget []byte `json:"budget"`
	}{s, budget})
	if err != nil {
		return "", fmt.Errorf("eventlog: digest: %w", err)
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
