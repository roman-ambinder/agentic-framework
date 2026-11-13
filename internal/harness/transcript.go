package harness

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// Transcript rebuilds the conversation from a run's events. Tool outputs are
// capped with toolrouter.ForModel(output, maxToolOutputBytes) exactly as the
// model saw them; maxToolOutputBytes <= 0 means toolrouter.DefaultMaxModelBytes.
func Transcript(events []*adlcv1.Event, maxToolOutputBytes int) ([]model.Message, error) {
	if len(events) == 0 || events[0].GetRunStarted() == nil {
		kind := "none"
		if len(events) > 0 {
			kind = eventlog.Kind(events[0])
		}
		return nil, fmt.Errorf("harness: transcript: first event is %s, want run_started", kind)
	}
	var msgs []model.Message
	var pending []model.Part // tool results waiting for the next message
	flush := func() {
		if len(pending) > 0 {
			msgs = append(msgs, model.Message{Role: model.RoleUser, Parts: pending})
			pending = nil
		}
	}
	for _, ev := range events {
		switch p := ev.GetPayload().(type) {
		case *adlcv1.Event_RunStarted:
			msgs = append(msgs, model.Message{
				Role: model.RoleUser, Parts: []model.Part{model.TextPart(p.RunStarted.GetTask())},
			})
		case *adlcv1.Event_ModelCallCompleted:
			flush()
			msgs = append(msgs, assistantMessage(p.ModelCallCompleted))
		case *adlcv1.Event_ToolCallCompleted:
			c := p.ToolCallCompleted
			text, _ := toolrouter.ForModel(c.GetOutput(), maxToolOutputBytes)
			pending = append(pending, model.ToolResultPart(model.ToolResult{
				CallID: c.GetCallId(), Content: text, IsError: c.GetIsError(),
			}))
		}
	}
	flush()
	return msgs, nil
}

// assistantMessage rebuilds the model's message: text, then tool calls, with
// provider blobs put back at their recorded positions.
func assistantMessage(c *adlcv1.ModelCallCompleted) model.Message {
	var parts []model.Part
	if c.GetText() != "" {
		parts = append(parts, model.TextPart(c.GetText()))
	}
	for _, tc := range c.GetToolCalls() {
		args := json.RawMessage(tc.GetArgumentsJson())
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		parts = append(parts, model.ToolCallPart(model.ToolCall{
			ID: tc.GetId(), Name: tc.GetName(), Arguments: args,
		}))
	}
	blobs := slices.Clone(c.GetProviderBlobs())
	slices.SortStableFunc(blobs, func(a, b *adlcv1.ProviderBlob) int {
		return cmp.Compare(a.GetIndex(), b.GetIndex())
	})
	for _, b := range blobs {
		at := len(parts)
		if idx := int(min(b.GetIndex(), uint32(len(parts)))); idx < at { //nolint:gosec // len(parts) is small
			at = idx
		}
		parts = slices.Insert(parts, at, model.BlobPart(model.Blob{
			Provider: b.GetProvider(), Kind: b.GetKind(), Data: b.GetData(),
		}))
	}
	return model.Message{Role: model.RoleAssistant, Parts: parts}
}
