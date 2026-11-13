package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/model/modeltest"
)

const errorBody = `{"error":{"message":"x","type":"server_error","code":null}}`

// fakeAPI answers POST /chat/completions with a canned status and body and
// records every request.
type fakeAPI struct {
	srv    *httptest.Server
	status int
	body   string
	mu     sync.Mutex
	got    [][]byte
	paths  []string
}

func newFakeAPI(t *testing.T, status int, body string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, b)
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, f.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func fixtureAPI(t *testing.T, name string) *fakeAPI {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return newFakeAPI(t, http.StatusOK, string(b))
}

func (f *fakeAPI) adapter(t *testing.T) *Adapter {
	t.Helper()
	a, err := New(Config{APIKey: "test-key", BaseURL: f.srv.URL, HTTPClient: f.srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *fakeAPI) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *fakeAPI) body0(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.got) == 0 {
		t.Fatal("no request recorded")
	}
	var m map[string]any
	if err := json.Unmarshal(f.got[0], &m); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return m
}

func userText(s string) model.Message {
	return model.Message{Role: model.RoleUser, Parts: []model.Part{model.TextPart(s)}}
}

func simpleReq() *model.Request {
	return &model.Request{Model: "test-model", Messages: []model.Message{userText("hi")}}
}

func generateFixture(t *testing.T, name string) *model.Response {
	t.Helper()
	f := fixtureAPI(t, name)
	resp, err := f.adapter(t).Generate(context.Background(), simpleReq())
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func jsonEq(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("arguments %s: %v", got, err)
	}
	_ = json.Unmarshal([]byte(want), &w)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("arguments = %s, want %s", got, want)
	}
}

func TestGenerateToolCall(t *testing.T) {
	resp := generateFixture(t, "tool_call.json")
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_TOOL_USE {
		t.Errorf("finish = %v", resp.Finish)
	}
	if resp.ProviderFinish != "tool_calls" {
		t.Errorf("provider finish = %q", resp.ProviderFinish)
	}
	if resp.Message.Text() != "Reading the file." {
		t.Errorf("text = %q", resp.Message.Text())
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "fs__read_file" {
		t.Fatalf("calls = %+v", calls)
	}
	jsonEq(t, calls[0].Arguments, `{"path":"main.go"}`)
	want := model.Usage{InputTokens: 20, CacheReadTokens: 100, OutputTokens: 25}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
	if resp.Provider != "openai" || resp.Model != "test-model" {
		t.Errorf("provider/model = %q/%q", resp.Provider, resp.Model)
	}
}

func TestGenerateEndTurn(t *testing.T) {
	resp := generateFixture(t, "end_turn.json")
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_END_TURN {
		t.Errorf("finish = %v", resp.Finish)
	}
	if resp.Message.Text() != "Done." || len(resp.Message.ToolCalls()) != 0 {
		t.Errorf("message = %+v", resp.Message)
	}
	if resp.Usage != (model.Usage{InputTokens: 50, OutputTokens: 3}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGenerateRefusal(t *testing.T) {
	resp := generateFixture(t, "refusal.json")
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_REFUSAL {
		t.Errorf("finish = %v", resp.Finish)
	}
	if resp.Message.Text() != "I can't help with that." {
		t.Errorf("text = %q", resp.Message.Text())
	}
}

func TestGenerateBadArguments(t *testing.T) {
	resp := generateFixture(t, "bad_arguments.json")
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	a := calls[0].Arguments
	if !json.Valid(a) || a[0] != '"' {
		t.Errorf("arguments = %s, want a valid JSON string", a)
	}
}

func TestGenerateNoChoices(t *testing.T) {
	f := newFakeAPI(t, 200, `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[]}`)
	_, err := f.adapter(t).Generate(context.Background(), simpleReq())
	var me *model.Error
	if !errors.As(err, &me) || !me.Retryable || me.Class != adlcv1.ErrorClass_ERROR_CLASS_MODEL {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestMapping(t *testing.T) {
	blob := []byte("SECRET-BLOB-BYTES")
	req := &model.Request{
		Model: "test-model", System: "sys",
		Messages: []model.Message{
			userText("first"),
			{Role: model.RoleAssistant, Parts: []model.Part{
				model.TextPart("thinking out loud"),
				model.ToolCallPart(model.ToolCall{ID: "call_1", Name: "a", Arguments: json.RawMessage(`{"x":1}`)}),
				model.ToolCallPart(model.ToolCall{ID: "call_2", Name: "b", Arguments: json.RawMessage(`{}`)}),
				model.BlobPart(model.Blob{Provider: "anthropic", Kind: "thinking", Data: blob}),
			}},
			{Role: model.RoleUser, Parts: []model.Part{
				model.TextPart("and also"),
				model.ToolResultPart(model.ToolResult{CallID: "call_1", Content: "ok"}),
				model.ToolResultPart(model.ToolResult{CallID: "call_2", Content: "boom", IsError: true}),
			}},
		},
	}
	f := fixtureAPI(t, "end_turn.json")
	if _, err := f.adapter(t).Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	body := f.body0(t)
	msgs := body["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if want := "system,user,assistant,tool,tool,user"; strings.Join(roles, ",") != want {
		t.Fatalf("roles = %v, want %s", roles, want)
	}
	asst := msgs[2].(map[string]any)
	if n := len(asst["tool_calls"].([]any)); n != 2 {
		t.Errorf("tool_calls = %d", n)
	}
	call := asst["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "call_1" || call["function"].(map[string]any)["arguments"] != `{"x":1}` {
		t.Errorf("call = %v", call)
	}
	t1, t2 := msgs[3].(map[string]any), msgs[4].(map[string]any)
	if t1["tool_call_id"] != "call_1" || t1["content"] != "ok" {
		t.Errorf("tool 1 = %v", t1)
	}
	if c, _ := t2["content"].(string); !strings.HasPrefix(c, "error: ") {
		t.Errorf("tool 2 content = %v", t2["content"])
	}
	if strings.Contains(string(f.got[0]), string(blob)) {
		t.Error("request contains blob bytes")
	}
}

func TestRequestSkipsEmptyAssistant(t *testing.T) {
	req := &model.Request{Model: "m", Messages: []model.Message{
		userText("a"),
		{Role: model.RoleAssistant, Parts: []model.Part{model.BlobPart(model.Blob{Data: []byte("x")})}},
		userText("b"),
	}}
	p, err := buildParams(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 2 {
		t.Errorf("messages = %d, want 2", len(p.Messages))
	}
}

func TestRequestTools(t *testing.T) {
	schema := `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`
	req := simpleReq()
	req.Tools = []model.ToolSpec{{Name: "fs__read_file", Description: "Read.", InputSchema: json.RawMessage(schema)}}
	f := fixtureAPI(t, "end_turn.json")
	if _, err := f.adapter(t).Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	body := f.body0(t)
	tool := body["tools"].([]any)[0].(map[string]any)
	fn := tool["function"].(map[string]any)
	var want any
	_ = json.Unmarshal([]byte(schema), &want)
	if tool["type"] != "function" || fn["name"] != "fs__read_file" || !reflect.DeepEqual(fn["parameters"], want) {
		t.Errorf("tool = %v", tool)
	}
	if body["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v", body["tool_choice"])
	}

	f2 := fixtureAPI(t, "end_turn.json")
	if _, err := f2.adapter(t).Generate(context.Background(), simpleReq()); err != nil {
		t.Fatal(err)
	}
	b2 := f2.body0(t)
	if _, ok := b2["tool_choice"]; ok {
		t.Error("tool_choice present without tools")
	}
	if _, ok := b2["tools"]; ok {
		t.Error("tools present without tools")
	}
}

func TestRequestOptionalFields(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens int
		effort    string
	}{
		{"set", 100, "high"},
		{"zero", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := simpleReq()
			req.MaxTokens, req.Effort = tt.maxTokens, tt.effort
			f := fixtureAPI(t, "end_turn.json")
			if _, err := f.adapter(t).Generate(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			body := f.body0(t)
			mt, hasMT := body["max_completion_tokens"]
			re, hasRE := body["reasoning_effort"]
			if tt.maxTokens > 0 {
				if mt != float64(100) || re != "high" {
					t.Errorf("max_completion_tokens = %v, reasoning_effort = %v", mt, re)
				}
				return
			}
			if hasMT || hasRE {
				t.Errorf("optional keys present: %v", body)
			}
		})
	}
}

func TestRequestBadSchema(t *testing.T) {
	req := simpleReq()
	req.Tools = []model.ToolSpec{{Name: "t", InputSchema: json.RawMessage(`[1,2]`)}}
	f := fixtureAPI(t, "end_turn.json")
	_, err := f.adapter(t).Generate(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "input schema is not a JSON object") {
		t.Fatalf("err = %v", err)
	}
	if f.requests() != 0 {
		t.Error("request was sent")
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		status    int
		class     adlcv1.ErrorClass
		retryable bool
	}{
		{429, adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, true},
		{500, adlcv1.ErrorClass_ERROR_CLASS_MODEL, true},
		{503, adlcv1.ErrorClass_ERROR_CLASS_MODEL, true},
		{400, adlcv1.ErrorClass_ERROR_CLASS_MODEL, false},
		{401, adlcv1.ErrorClass_ERROR_CLASS_MODEL, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			f := newFakeAPI(t, tt.status, errorBody)
			_, err := f.adapter(t).Generate(context.Background(), simpleReq())
			var me *model.Error
			if !errors.As(err, &me) {
				t.Fatalf("err = %v", err)
			}
			if me.Class != tt.class || me.HTTPStatus != tt.status || me.Retryable != tt.retryable {
				t.Errorf("got {%v %d %v}", me.Class, me.HTTPStatus, me.Retryable)
			}
		})
	}
}

func TestNetworkErrorIsRetryableTimeout(t *testing.T) {
	f := fixtureAPI(t, "end_turn.json")
	a := f.adapter(t)
	f.srv.Close()
	_, err := a.Generate(context.Background(), simpleReq())
	var me *model.Error
	if !errors.As(err, &me) || me.Class != adlcv1.ErrorClass_ERROR_CLASS_TIMEOUT || !me.Retryable {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelledContext(t *testing.T) {
	f := fixtureAPI(t, "end_turn.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.adapter(t).Generate(ctx, simpleReq())
	var me *model.Error
	if !errors.Is(err, context.Canceled) || errors.As(err, &me) || model.IsRetryable(err) {
		t.Fatalf("err = %v, retryable = %v", err, model.IsRetryable(err))
	}
}

func TestNoRetriesInSDK(t *testing.T) {
	f := newFakeAPI(t, 500, errorBody)
	if _, err := f.adapter(t).Generate(context.Background(), simpleReq()); err == nil {
		t.Fatal("want error")
	}
	if n := f.requests(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestBaseURL(t *testing.T) {
	f := fixtureAPI(t, "end_turn.json")
	if _, err := f.adapter(t).Generate(context.Background(), simpleReq()); err != nil {
		t.Fatal(err)
	}
	if len(f.paths) != 1 || !strings.HasSuffix(f.paths[0], "/chat/completions") {
		t.Errorf("paths = %v", f.paths)
	}
}

func TestCountTokensEstimate(t *testing.T) {
	a, err := New(Config{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	req := simpleReq()
	req.System = "You are a test."
	got, err := a.CountTokens(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := buildParams(req)
	b, _ := json.Marshal(p)
	if want := int64((len(b) + 3) / 4); got <= 0 || got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestCapabilities(t *testing.T) {
	a, _ := New(Config{MaxContextTokens: 1000})
	want := model.Caps{ToolCalling: true, PromptCaching: true, MaxContextTokens: 1000}
	if a.Capabilities() != want || a.Provider() != "openai" {
		t.Errorf("caps = %+v, provider = %q", a.Capabilities(), a.Provider())
	}
}

func completion(finish, content, toolCalls string, prompt, out int) string {
	tc := ""
	if toolCalls != "" {
		tc = `,"tool_calls":[` + toolCalls + `]`
	}
	cj := "null"
	if content != "" {
		cj = fmt.Sprintf("%q", content)
	}
	return fmt.Sprintf(`{"id":"c","object":"chat.completion","created":1,"model":"test-model",
"choices":[{"index":0,"finish_reason":%q,"message":{"role":"assistant","content":%s,"refusal":null%s}}],
"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`, finish, cj, tc, prompt, out, prompt+out)
}

func toolCallJSON(id, path string) string {
	return fmt.Sprintf(`{"id":%q,"type":"function","function":{"name":"fs__read_file","arguments":"{\"path\":\"%s\"}"}}`, id, path)
}

// contractAPI returns the canned Chat Completions answer for a scenario.
func contractAPI(t *testing.T, sc modeltest.Scenario) *fakeAPI {
	t.Helper()
	switch sc.Name {
	case modeltest.TextReply:
		return newFakeAPI(t, 200, completion("stop", "Hello!", "", 10, 3))
	case modeltest.ToolCall:
		return newFakeAPI(t, 200, completion("tool_calls", "", toolCallJSON("call_1", "main.go"), 20, 8))
	case modeltest.ParallelToolCalls:
		tcs := toolCallJSON("call_1", "a.go") + "," + toolCallJSON("call_2", "b.go")
		return newFakeAPI(t, 200, completion("tool_calls", "", tcs, 20, 16))
	case modeltest.ToolResultRoundTrip:
		return newFakeAPI(t, 200, completion("stop", "The file has 3 lines.", "", 40, 7))
	case modeltest.Refusal:
		return newFakeAPI(t, 200, completion("content_filter", "", "", 5, 0))
	case modeltest.MaxTokens:
		return newFakeAPI(t, 200, completion("length", "Once upon", "", 6, 1024))
	case modeltest.RateLimited:
		return newFakeAPI(t, 429, errorBody)
	case modeltest.ServerError:
		return newFakeAPI(t, 500, errorBody)
	case modeltest.BadRequest:
		return newFakeAPI(t, 400, errorBody)
	}
	t.Fatalf("no canned response for scenario %q", sc.Name)
	return nil
}

func TestContract(t *testing.T) {
	modeltest.Run(t, func(t *testing.T, sc modeltest.Scenario) model.Adapter {
		return contractAPI(t, sc).adapter(t)
	})
}
