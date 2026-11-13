// Package modeltest holds the contract scenarios every model adapter must
// pass. An adapter's test serves canned provider responses for each scenario
// (for example from an httptest.Server) and calls Run.
package modeltest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// Scenario is one model call and the provider-neutral result an adapter must
// produce for it. Exactly one of Want and WantErr is set.
type Scenario struct {
	Name    string
	Request *model.Request
	Want    *model.Response
	WantErr *model.Error // only Class, HTTPStatus and Retryable are compared
}

// Scenario names. Adapter tests key their canned provider responses by these.
const (
	TextReply           = "text_reply"
	ToolCall            = "tool_call"
	ParallelToolCalls   = "parallel_tool_calls"
	ToolResultRoundTrip = "tool_result_round_trip"
	Refusal             = "refusal"
	MaxTokens           = "max_tokens"
	RateLimited         = "rate_limited"
	ServerError         = "server_error"
	BadRequest          = "bad_request"
)

const readFileSchema = `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`

func readFileTool() model.ToolSpec {
	return model.ToolSpec{
		Name:        "fs__read_file",
		Description: "Read a file.",
		InputSchema: json.RawMessage(readFileSchema),
	}
}

func request(tools []model.ToolSpec, msgs ...model.Message) *model.Request {
	return &model.Request{
		Model:     "test-model",
		System:    "You are a test.",
		Messages:  msgs,
		Tools:     tools,
		MaxTokens: 1024,
	}
}

func userText(s string) model.Message {
	return model.Message{Role: model.RoleUser, Parts: []model.Part{model.TextPart(s)}}
}

func call(id, path string) model.ToolCall {
	return model.ToolCall{ID: id, Name: "fs__read_file", Arguments: json.RawMessage(`{"path":"` + path + `"}`)}
}

func reply(finish adlcv1.FinishReason, in, out int64, parts ...model.Part) *model.Response {
	return &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Parts: parts},
		Finish:  finish,
		Usage:   model.Usage{InputTokens: in, OutputTokens: out},
	}
}

func failure(class adlcv1.ErrorClass, status int, retryable bool) *model.Error {
	return &model.Error{Class: class, HTTPStatus: status, Retryable: retryable}
}

// Standard returns all scenarios, in the order of the name constants.
func Standard() []Scenario {
	tools := []model.ToolSpec{readFileTool()}
	hello := request(nil, userText("Say hello."))
	const (
		endTurn   = adlcv1.FinishReason_FINISH_REASON_END_TURN
		toolUse   = adlcv1.FinishReason_FINISH_REASON_TOOL_USE
		maxTokens = adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS
		refusal   = adlcv1.FinishReason_FINISH_REASON_REFUSAL
	)
	roundTrip := request(tools,
		userText("Read main.go."),
		model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.ToolCallPart(call("call_1", "main.go"))}},
		model.Message{Role: model.RoleUser, Parts: []model.Part{model.ToolResultPart(model.ToolResult{CallID: "call_1", Content: "line1\nline2\nline3"})}},
	)
	return []Scenario{
		{
			Name: TextReply, Request: hello,
			Want: reply(endTurn, 10, 3, model.TextPart("Hello!")),
		},
		{
			Name: ToolCall, Request: request(tools, userText("Read main.go.")),
			Want: reply(toolUse, 20, 8, model.ToolCallPart(call("call_1", "main.go"))),
		},
		{
			Name: ParallelToolCalls, Request: request(tools, userText("Read a.go and b.go.")),
			Want: reply(toolUse, 20, 16, model.ToolCallPart(call("call_1", "a.go")), model.ToolCallPart(call("call_2", "b.go"))),
		},
		{
			Name: ToolResultRoundTrip, Request: roundTrip,
			Want: reply(endTurn, 40, 7, model.TextPart("The file has 3 lines.")),
		},
		{
			Name: Refusal, Request: request(nil, userText("Do something harmful.")),
			Want: reply(refusal, 5, 0),
		},
		{
			Name: MaxTokens, Request: request(nil, userText("Write a long story.")),
			Want: reply(maxTokens, 6, 1024, model.TextPart("Once upon")),
		},
		{
			Name: RateLimited, Request: hello,
			WantErr: failure(adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, 429, true),
		},
		{
			Name: ServerError, Request: hello,
			WantErr: failure(adlcv1.ErrorClass_ERROR_CLASS_MODEL, 500, true),
		},
		{
			Name: BadRequest, Request: hello,
			WantErr: failure(adlcv1.ErrorClass_ERROR_CLASS_MODEL, 400, false),
		},
	}
}

// Run runs every scenario in Standard as a subtest named after it.
// newAdapter must return an adapter whose provider (usually an
// httptest.Server) answers that scenario. Run calls Generate once and checks
// the result with CheckResponse or CheckError.
func Run(t *testing.T, newAdapter func(t *testing.T, sc Scenario) model.Adapter) {
	t.Helper()
	for _, sc := range Standard() {
		t.Run(sc.Name, func(t *testing.T) {
			adapter := newAdapter(t, sc)
			got, err := adapter.Generate(context.Background(), sc.Request)
			if sc.WantErr != nil {
				if err == nil {
					t.Fatalf("Generate succeeded, want error %v", sc.WantErr)
				}
				CheckError(t, err, sc.WantErr)
				return
			}
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			CheckResponse(t, got, sc.Want)
		})
	}
}

// CheckResponse fails t if got differs from want. It ignores ProviderFinish,
// Provider, Model and Blob.Data, and compares ToolCall.Arguments as JSON
// values (key order and whitespace do not matter).
func CheckResponse(t testing.TB, got, want *model.Response) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Errorf("response = %v, want %v", got, want)
		}
		return
	}
	if got.Finish != want.Finish {
		t.Errorf("finish = %v, want %v", got.Finish, want.Finish)
	}
	if got.Usage != want.Usage {
		t.Errorf("usage = %+v, want %+v", got.Usage, want.Usage)
	}
	if got.Message.Role != want.Message.Role {
		t.Errorf("role = %q, want %q", got.Message.Role, want.Message.Role)
	}
	if len(got.Message.Parts) != len(want.Message.Parts) {
		t.Errorf("got %d parts, want %d", len(got.Message.Parts), len(want.Message.Parts))
		return
	}
	for i := range want.Message.Parts {
		checkPart(t, i, got.Message.Parts[i], want.Message.Parts[i])
	}
}

func checkPart(t testing.TB, i int, got, want model.Part) {
	t.Helper()
	if got.Kind != want.Kind {
		t.Errorf("part %d kind = %q, want %q", i, got.Kind, want.Kind)
		return
	}
	switch want.Kind {
	case model.PartText:
		if got.Text != want.Text {
			t.Errorf("part %d text = %q, want %q", i, got.Text, want.Text)
		}
	case model.PartToolCall:
		checkToolCall(t, i, got.ToolCall, want.ToolCall)
	case model.PartToolResult:
		if !reflect.DeepEqual(got.ToolResult, want.ToolResult) {
			t.Errorf("part %d tool result = %+v, want %+v", i, got.ToolResult, want.ToolResult)
		}
	case model.PartBlob:
		if got.Blob == nil || want.Blob == nil {
			if got.Blob != want.Blob {
				t.Errorf("part %d blob = %v, want %v", i, got.Blob, want.Blob)
			}
			return
		}
		if got.Blob.Provider != want.Blob.Provider || got.Blob.Kind != want.Blob.Kind {
			t.Errorf("part %d blob = {%s %s}, want {%s %s}", i, got.Blob.Provider, got.Blob.Kind, want.Blob.Provider, want.Blob.Kind)
		}
	}
}

func checkToolCall(t testing.TB, i int, got, want *model.ToolCall) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Errorf("part %d tool call = %v, want %v", i, got, want)
		}
		return
	}
	if got.ID != want.ID || got.Name != want.Name {
		t.Errorf("part %d tool call = {%s %s}, want {%s %s}", i, got.ID, got.Name, want.ID, want.Name)
	}
	if !jsonEqual(got.Arguments, want.Arguments) {
		t.Errorf("part %d arguments = %s, want %s", i, got.Arguments, want.Arguments)
	}
}

func jsonEqual(a, b []byte) bool {
	var av, bv any
	if err := json.NewDecoder(bytes.NewReader(a)).Decode(&av); err != nil {
		return false
	}
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// CheckError fails t unless err is a *model.Error with want's Class,
// HTTPStatus and Retryable.
func CheckError(t testing.TB, err error, want *model.Error) {
	t.Helper()
	var got *model.Error
	if !errors.As(err, &got) {
		t.Errorf("error %v is not a *model.Error", err)
		return
	}
	if got.Class != want.Class || got.HTTPStatus != want.HTTPStatus || got.Retryable != want.Retryable {
		t.Errorf("error = {%v %d %v}, want {%v %d %v}",
			got.Class, got.HTTPStatus, got.Retryable, want.Class, want.HTTPStatus, want.Retryable)
	}
}
