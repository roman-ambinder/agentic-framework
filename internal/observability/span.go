// Package observability turns a run's event log into a span tree, prints it
// as text and exports it as OpenTelemetry spans. Spans are built after the
// fact from events; nothing is traced live.
package observability

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// SpanKind is what a Span represents.
type SpanKind string

// Span kinds.
const (
	KindRun   SpanKind = "run"
	KindModel SpanKind = "model"
	KindTool  SpanKind = "tool"
)

// Span is one node of a run's trace.
type Span struct {
	Kind     SpanKind
	Name     string
	Start    time.Time
	End      time.Time // zero if the span never ended (run still going, call in flight)
	Attrs    []attribute.KeyValue
	Failed   bool        // true means OTel status Error
	Status   string      // short text for the tree
	Events   []SpanEvent // point-in-time events, in log order
	Children []*Span     // run span only; in log order of their first event
}

// SpanEvent is a point-in-time event on a span.
type SpanEvent struct {
	Name  string
	Time  time.Time
	Attrs []attribute.KeyValue
}

// ErrNotARun is returned by Build when the events do not start with run_started.
var ErrNotARun = errors.New("observability: events do not start with run_started")

// builder holds the state of one Build call.
type builder struct {
	root      *Span
	openModel map[string]*Span
	openTool  map[string]*Span
	completed bool
}

// Build turns one run's events (seq order, from seq 1) into a span tree.
func Build(events []*adlcv1.Event) (*Span, error) {
	if len(events) == 0 || events[0].GetRunStarted() == nil {
		return nil, ErrNotARun
	}
	first := events[0]
	rs := first.GetRunStarted()
	b := &builder{
		root: &Span{
			Kind:  KindRun,
			Name:  "run " + first.GetRunId(),
			Start: first.GetTime().AsTime(),
			Attrs: []attribute.KeyValue{
				attribute.String("adlc.run_id", first.GetRunId()),
				attribute.String("adlc.agent_id", first.GetAgentId()),
				attribute.String("adlc.agent_version", rs.GetAgentVersion()),
				attribute.String("adlc.task", rs.GetTask()),
			},
		},
		openModel: map[string]*Span{},
		openTool:  map[string]*Span{},
	}
	for _, ev := range events {
		if err := b.apply(ev); err != nil {
			return nil, err
		}
	}
	return b.finish(events)
}

func (b *builder) apply(ev *adlcv1.Event) error {
	t := ev.GetTime().AsTime()
	switch p := ev.GetPayload().(type) {
	case *adlcv1.Event_ModelCallStarted:
		b.modelStarted(p.ModelCallStarted, t)
	case *adlcv1.Event_ModelCallCompleted:
		return b.modelCompleted(ev, p.ModelCallCompleted, t)
	case *adlcv1.Event_ModelCallFailed:
		return b.modelFailed(ev, p.ModelCallFailed, t)
	case *adlcv1.Event_ToolCallRequested:
		b.toolRequested(p.ToolCallRequested, t)
	case *adlcv1.Event_PolicyDecided:
		b.policyDecided(p.PolicyDecided, t)
	case *adlcv1.Event_ToolCallCompleted:
		return b.toolCompleted(ev, p.ToolCallCompleted, t)
	case *adlcv1.Event_ApprovalRequested:
		a := p.ApprovalRequested
		b.rootEvent("approval_requested", t,
			attribute.String("adlc.approval_id", a.GetApprovalId()),
			attribute.String("adlc.call_id", a.GetCallId()),
			attribute.String("adlc.summary", a.GetSummary()))
	case *adlcv1.Event_ApprovalResolved:
		a := p.ApprovalResolved
		b.rootEvent("approval_resolved", t,
			attribute.String("adlc.approval_id", a.GetApprovalId()),
			attribute.Bool("adlc.approved", a.GetApproved()),
			attribute.String("adlc.resolved_by", a.GetResolvedBy()),
			attribute.String("adlc.comment", a.GetComment()))
	case *adlcv1.Event_Checkpoint:
		c := p.Checkpoint
		b.rootEvent("checkpoint", t,
			attribute.Int64("adlc.step", int64(c.GetStep())),
			attribute.String("adlc.snapshot_ref", c.GetSnapshotRef()),
			attribute.String("adlc.state_digest", c.GetStateDigest()))
	case *adlcv1.Event_ErrorOccurred:
		e := p.ErrorOccurred
		b.rootEvent("error", t,
			attribute.String("adlc.error_class", e.GetErrorClass().String()),
			attribute.String("adlc.message", e.GetMessage()),
			attribute.String("adlc.component", e.GetComponent()))
		b.root.Failed = true
	case *adlcv1.Event_RunCompleted:
		b.runCompleted(p.RunCompleted, t)
	}
	return nil
}

func (b *builder) rootEvent(name string, t time.Time, attrs ...attribute.KeyValue) {
	b.root.Events = append(b.root.Events, SpanEvent{Name: name, Time: t, Attrs: attrs})
}

func (b *builder) modelStarted(m *adlcv1.ModelCallStarted, t time.Time) {
	id := m.GetCallId()
	if old := b.openModel[id]; old != nil {
		old.End, old.Status = t, "superseded"
	}
	s := &Span{
		Kind: KindModel, Name: "model " + id, Start: t,
		Attrs: []attribute.KeyValue{
			attribute.String("adlc.call_id", id),
			attribute.Int64("adlc.step", int64(m.GetStep())),
			attribute.Int64("adlc.attempt", int64(m.GetAttempt())),
			attribute.String("adlc.provider", m.GetProvider()),
			attribute.String("adlc.model", m.GetModel()),
		},
	}
	b.openModel[id] = s
	b.root.Children = append(b.root.Children, s)
}

func (b *builder) modelCompleted(ev *adlcv1.Event, m *adlcv1.ModelCallCompleted, t time.Time) error {
	id := m.GetCallId()
	s := b.openModel[id]
	if s == nil {
		return fmt.Errorf("observability: seq %d: model_call_completed for %q without start", ev.GetSeq(), id)
	}
	c := ev.GetCost()
	s.End = t
	s.Attrs = append(s.Attrs,
		attribute.String("adlc.finish_reason", m.GetFinishReason().String()),
		attribute.Int64("adlc.input_tokens", c.GetInputTokens()),
		attribute.Int64("adlc.output_tokens", c.GetOutputTokens()),
		attribute.Int64("adlc.cache_read_tokens", c.GetCacheReadTokens()),
		attribute.Int64("adlc.cache_write_tokens", c.GetCacheWriteTokens()),
		attribute.Int64("adlc.usd_micros", c.GetUsdMicros()))
	s.Status = short(m.GetFinishReason().String(), "FINISH_REASON_")
	delete(b.openModel, id)
	return nil
}

func (b *builder) modelFailed(ev *adlcv1.Event, m *adlcv1.ModelCallFailed, t time.Time) error {
	id := m.GetCallId()
	s := b.openModel[id]
	if s == nil {
		return fmt.Errorf("observability: seq %d: model_call_failed for %q without start", ev.GetSeq(), id)
	}
	s.End, s.Failed = t, true
	s.Attrs = append(s.Attrs,
		attribute.String("adlc.error_class", m.GetErrorClass().String()),
		attribute.Bool("adlc.will_retry", m.GetWillRetry()),
		attribute.Int64("adlc.http_status", int64(m.GetHttpStatus())))
	s.Status = "failed: " + m.GetMessage()
	delete(b.openModel, id)
	return nil
}

func (b *builder) toolRequested(r *adlcv1.ToolCallRequested, t time.Time) {
	c := r.GetCall()
	s := &Span{
		Kind: KindTool, Name: "tool " + c.GetId() + " " + c.GetName(), Start: t,
		Attrs: []attribute.KeyValue{
			attribute.String("adlc.call_id", c.GetId()),
			attribute.String("adlc.tool", c.GetName()),
			attribute.String("adlc.server", r.GetServer()),
			attribute.String("adlc.risk_class", r.GetRiskClass().String()),
		},
	}
	b.openTool[c.GetId()] = s
	b.root.Children = append(b.root.Children, s)
}

func (b *builder) policyDecided(p *adlcv1.PolicyDecided, t time.Time) {
	se := SpanEvent{Name: "policy", Time: t, Attrs: []attribute.KeyValue{
		attribute.String("adlc.decision", p.GetDecision().String()),
		attribute.String("adlc.rule_id", p.GetRuleId()),
		attribute.String("adlc.reason", p.GetReason()),
		attribute.String("adlc.source", p.GetSource().String()),
	}}
	s := b.openTool[p.GetCallId()]
	if s == nil {
		b.root.Events = append(b.root.Events, se)
		return
	}
	s.Events = append(s.Events, se)
	if p.GetDecision() == adlcv1.Decision_DECISION_DENY {
		s.End, s.Failed, s.Status = t, true, "denied"
		delete(b.openTool, p.GetCallId())
	}
}

func (b *builder) toolCompleted(ev *adlcv1.Event, c *adlcv1.ToolCallCompleted, t time.Time) error {
	id := c.GetCallId()
	s := b.openTool[id]
	if s == nil {
		return fmt.Errorf("observability: seq %d: tool_call_completed for %q without request", ev.GetSeq(), id)
	}
	s.End = t
	s.Attrs = append(s.Attrs,
		attribute.Bool("adlc.is_error", c.GetIsError()),
		attribute.Int64("adlc.exit_code", int64(c.GetExitCode())),
		attribute.Int64("adlc.output_bytes", int64(len(c.GetOutput()))),
		attribute.Bool("adlc.truncated_for_model", c.GetTruncatedForModel()))
	s.Status, s.Failed = "ok", c.GetIsError()
	if c.GetIsError() {
		s.Status = "error"
	}
	delete(b.openTool, id)
	return nil
}

func (b *builder) runCompleted(r *adlcv1.RunCompleted, t time.Time) {
	b.completed = true
	b.root.End = t
	b.root.Attrs = append(b.root.Attrs, attribute.String("adlc.stop_reason", r.GetStopReason().String()))
	if r.GetExhaustedBudget() != adlcv1.BudgetKind_BUDGET_KIND_UNSPECIFIED {
		b.root.Attrs = append(b.root.Attrs, attribute.String("adlc.exhausted_budget", r.GetExhaustedBudget().String()))
	}
	b.root.Status = short(r.GetStopReason().String(), "STOP_REASON_")
	b.root.Failed = r.GetStopReason() == adlcv1.StopReason_STOP_REASON_ERROR
}

func (b *builder) finish(events []*adlcv1.Event) (*Span, error) {
	// Children are walked in order, never the maps, so output is deterministic.
	for _, c := range b.root.Children {
		if c.End.IsZero() && c.Status == "" {
			c.Status = "in flight"
		}
	}
	state, err := eventlog.Fold(events)
	if err != nil {
		return nil, fmt.Errorf("observability: %w", err)
	}
	b.root.Attrs = append(b.root.Attrs,
		attribute.Int64("adlc.input_tokens", state.Usage.InputTokens),
		attribute.Int64("adlc.output_tokens", state.Usage.OutputTokens),
		attribute.Int64("adlc.usd_micros", state.Usage.USDMicros),
		attribute.String("adlc.status", state.Status.String()))
	if !b.completed {
		b.root.Status = "running"
	}
	return b.root, nil
}

// short strips prefix from an enum name and lowercases it.
func short(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}
