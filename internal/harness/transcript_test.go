package harness

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

func evStarted() *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{Task: "task"}}}
}

func evModel(c *adlcv1.ModelCallCompleted) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: c}}
}

func evToolDone(id, out string, isErr bool) *adlcv1.Event {
	return &adlcv1.Event{Payload: &adlcv1.Event_ToolCallCompleted{ToolCallCompleted: &adlcv1.ToolCallCompleted{
		CallId: id, Output: []byte(out), IsError: isErr,
	}}}
}

func wireCall(id, name, args string) *adlcv1.ToolCall {
	return &adlcv1.ToolCall{Id: id, Name: name, ArgumentsJson: args}
}

func TestTranscriptMapsEvents(t *testing.T) {
	got, err := Transcript([]*adlcv1.Event{
		evStarted(),
		evModel(&adlcv1.ModelCallCompleted{Text: "thinking", ToolCalls: []*adlcv1.ToolCall{wireCall("c1", "fs__read_file", `{"path":"a"}`)}}),
		evToolDone("c1", "out", true),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.Message{
		{Role: model.RoleUser, Parts: []model.Part{model.TextPart("task")}},
		{Role: model.RoleAssistant, Parts: []model.Part{
			model.TextPart("thinking"),
			model.ToolCallPart(model.ToolCall{ID: "c1", Name: "fs__read_file", Arguments: json.RawMessage(`{"path":"a"}`)}),
		}},
		{Role: model.RoleUser, Parts: []model.Part{
			model.ToolResultPart(model.ToolResult{CallID: "c1", Content: "out", IsError: true}),
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestTranscriptGroupsToolResults(t *testing.T) {
	got, err := Transcript([]*adlcv1.Event{
		evStarted(),
		evModel(&adlcv1.ModelCallCompleted{ToolCalls: []*adlcv1.ToolCall{wireCall("c1", "a", "{}"), wireCall("c2", "b", "{}")}}),
		evToolDone("c1", "one", false),
		evToolDone("c2", "two", false),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || len(got[2].Parts) != 2 {
		t.Fatalf("got %+v, want 3 messages, last with 2 results", got)
	}
	if got[2].Parts[0].ToolResult.CallID != "c1" || got[2].Parts[1].ToolResult.CallID != "c2" {
		t.Fatalf("results out of order: %+v", got[2].Parts)
	}
}

func TestTranscriptFlushesResultsBeforeNextAssistantMessage(t *testing.T) {
	got, err := Transcript([]*adlcv1.Event{
		evStarted(),
		evModel(&adlcv1.ModelCallCompleted{ToolCalls: []*adlcv1.ToolCall{wireCall("c1", "a", "{}")}}),
		evToolDone("c1", "one", false),
		evModel(&adlcv1.ModelCallCompleted{Text: "done"}),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	roles := []model.Role{model.RoleUser, model.RoleAssistant, model.RoleUser, model.RoleAssistant}
	if len(got) != len(roles) {
		t.Fatalf("got %d messages, want %d", len(got), len(roles))
	}
	for i, r := range roles {
		if got[i].Role != r {
			t.Fatalf("message %d role = %s, want %s", i, got[i].Role, r)
		}
	}
}

func TestTranscriptRestoresBlobOrder(t *testing.T) {
	got, err := Transcript([]*adlcv1.Event{
		evStarted(),
		evModel(&adlcv1.ModelCallCompleted{
			Text:      "hi",
			ToolCalls: []*adlcv1.ToolCall{wireCall("c1", "a", "{}")},
			// Out of order on purpose: they are inserted in ascending index.
			ProviderBlobs: []*adlcv1.ProviderBlob{
				{Provider: "p", Kind: "b5", Data: []byte("5"), Index: 5},
				{Provider: "p", Kind: "b0", Data: []byte("0"), Index: 0},
			},
		}),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	parts := got[1].Parts
	var order []string
	for _, p := range parts {
		switch p.Kind {
		case model.PartBlob:
			order = append(order, p.Blob.Kind)
		case model.PartText:
			order = append(order, "text")
		case model.PartToolCall:
			order = append(order, "call")
		}
	}
	if want := []string{"b0", "text", "call", "b5"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	if parts[0].Blob.Provider != "p" || string(parts[0].Blob.Data) != "0" {
		t.Fatalf("blob fields lost: %+v", parts[0].Blob)
	}
}

func TestTranscriptCapsToolOutput(t *testing.T) {
	out := strings.Repeat("x", 100)
	got, err := Transcript([]*adlcv1.Event{
		evStarted(),
		evModel(&adlcv1.ModelCallCompleted{ToolCalls: []*adlcv1.ToolCall{wireCall("c1", "a", "{}")}}),
		evToolDone("c1", out, false),
	}, 10)
	if err != nil {
		t.Fatal(err)
	}
	want, truncated := toolrouter.ForModel([]byte(out), 10)
	if !truncated {
		t.Fatal("test setup: expected truncation")
	}
	if c := got[2].Parts[0].ToolResult.Content; c != want {
		t.Fatalf("content = %q, want %q", c, want)
	}
}

func TestTranscriptIgnoresOtherKinds(t *testing.T) {
	got, err := Transcript([]*adlcv1.Event{
		evStarted(),
		{Payload: &adlcv1.Event_PolicyDecided{PolicyDecided: &adlcv1.PolicyDecided{CallId: "c"}}},
		{Payload: &adlcv1.Event_Checkpoint{Checkpoint: &adlcv1.Checkpoint{Step: 1}}},
		{Payload: &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{Message: "x"}}},
		{Payload: &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{CallId: "m1"}}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d messages, want only the task", len(got))
	}
}

func TestTranscriptRequiresRunStarted(t *testing.T) {
	_, err := Transcript([]*adlcv1.Event{
		{Payload: &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{}}},
	}, 0)
	want := "harness: transcript: first event is error_occurred, want run_started"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestTranscriptEmptyArguments(t *testing.T) {
	got, err := Transcript([]*adlcv1.Event{
		evStarted(),
		evModel(&adlcv1.ModelCallCompleted{ToolCalls: []*adlcv1.ToolCall{wireCall("c1", "a", "")}}),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if a := string(got[1].Parts[0].ToolCall.Arguments); a != "{}" {
		t.Fatalf("arguments = %q, want {}", a)
	}
}

func TestTranscriptInsertsBlobsInAscendingIndexOrder(t *testing.T) {
	got, err := Transcript([]*adlcv1.Event{
		evStarted(),
		evModel(&adlcv1.ModelCallCompleted{
			Text:      "hi",
			ToolCalls: []*adlcv1.ToolCall{wireCall("c1", "a", "{}")},
			ProviderBlobs: []*adlcv1.ProviderBlob{
				{Kind: "b1", Index: 1},
				{Kind: "b0", Index: 0},
			},
		}),
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, p := range got[1].Parts {
		if p.Kind == model.PartBlob {
			order = append(order, p.Blob.Kind)
		} else {
			order = append(order, string(p.Kind))
		}
	}
	if want := []string{"b0", "b1", "text", "tool_call"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}
