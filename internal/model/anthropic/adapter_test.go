package anthropic

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

	sdk "github.com/anthropics/anthropic-sdk-go/v2"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/model/modeltest"
)

// fakeAPI serves one canned response per request from bodies (in order; the
// last one repeats), records each request body, and answers with status codes
// from statuses (200 if shorter).
type fakeAPI struct {
	srv      *httptest.Server
	bodies   []string
	statuses []int
	got      [][]byte
	paths    []string
	mu       sync.Mutex
}

func newFakeAPI(t *testing.T, statuses []int, bodies ...string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{bodies: bodies, statuses: statuses}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		i := len(f.got)
		f.got = append(f.got, b)
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if r.Method != http.MethodPost || (r.URL.Path != "/v1/messages" && r.URL.Path != "/v1/messages/count_tokens") {
			http.NotFound(w, r)
			return
		}
		status := http.StatusOK
		if i < len(f.statuses) {
			status = f.statuses[i]
		}
		body := ""
		if len(f.bodies) > 0 {
			body = f.bodies[min(i, len(f.bodies)-1)]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) adapter() *Adapter { return New(Options{APIKey: "test-key", BaseURL: f.srv.URL}) }

func (f *fakeAPI) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// body decodes the i-th recorded request body.
func (f *fakeAPI) body(t *testing.T, i int) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.got) {
		t.Fatalf("only %d requests recorded, want index %d", len(f.got), i)
	}
	var m map[string]any
	if err := json.Unmarshal(f.got[i], &m); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return m
}

func testdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func userText(s string) model.Message {
	return model.Message{Role: model.RoleUser, Parts: []model.Part{model.TextPart(s)}}
}

func simpleReq(msgs ...model.Message) *model.Request {
	return &model.Request{Model: "test-model", MaxTokens: 100, Messages: msgs}
}

// diff returns "" when want and got are deeply equal, else a description.
func diff(want, got any) string {
	if reflect.DeepEqual(want, got) {
		return ""
	}
	return fmt.Sprintf("want %#v, got %#v", want, got)
}

func get(t *testing.T, v any, path ...any) any {
	t.Helper()
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				t.Fatalf("path %v: %v is not an object", path, v)
			}
			v = m[k]
		case int:
			s, ok := v.([]any)
			if !ok || k >= len(s) {
				t.Fatalf("path %v: %v is not an array with index %d", path, v, k)
			}
			v = s[k]
		}
	}
	return v
}

func TestProviderAndCapabilities(t *testing.T) {
	a := New(Options{APIKey: "test-key", MaxContextTokens: 123})
	if a.Provider() != "anthropic" {
		t.Errorf("Provider = %q", a.Provider())
	}
	want := model.Caps{ToolCalling: true, PromptCaching: true, Streaming: false, MaxContextTokens: 123}
	if got := a.Capabilities(); got != want {
		t.Errorf("Capabilities = %+v, want %+v", got, want)
	}
}

func TestGenerateText(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "text.json"))
	resp, err := f.adapter().Generate(context.Background(), simpleReq(userText("hi")))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.Parts) != 1 || resp.Message.Parts[0].Kind != model.PartText || resp.Message.Parts[0].Text != "hello" {
		t.Errorf("parts = %+v", resp.Message.Parts)
	}
	if resp.Message.Role != model.RoleAssistant {
		t.Errorf("role = %q", resp.Message.Role)
	}
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_END_TURN || resp.ProviderFinish != "end_turn" {
		t.Errorf("finish = %v %q", resp.Finish, resp.ProviderFinish)
	}
	if want := (model.Usage{InputTokens: 12, OutputTokens: 3}); resp.Usage != want {
		t.Errorf("usage = %+v", resp.Usage)
	}
	if resp.Provider != "anthropic" || resp.Model != "test-model" {
		t.Errorf("provider/model = %q/%q", resp.Provider, resp.Model)
	}
}

func TestGenerateToolUse(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "tool_use.json"))
	resp, err := f.adapter().Generate(context.Background(), simpleReq(userText("hi")))
	if err != nil {
		t.Fatal(err)
	}
	parts := resp.Message.Parts
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}
	if parts[0].Kind != model.PartBlob || parts[0].Blob.Kind != "thinking" || parts[0].Blob.Provider != "anthropic" {
		t.Errorf("part 0 = %+v", parts[0])
	}
	if parts[1].Kind != model.PartText || parts[1].Text != "Reading." {
		t.Errorf("part 1 = %+v", parts[1])
	}
	c := parts[2].ToolCall
	if parts[2].Kind != model.PartToolCall || c == nil || c.ID != "toolu_1" || c.Name != "fs__read_file" {
		t.Fatalf("part 2 = %+v", parts[2])
	}
	var args map[string]any
	if err := json.Unmarshal(c.Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if diff := diff(map[string]any{"path": "a.txt"}, args); diff != "" {
		t.Errorf("arguments: %s", diff)
	}
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_TOOL_USE {
		t.Errorf("finish = %v", resp.Finish)
	}
	if want := (model.Usage{InputTokens: 40, OutputTokens: 20, CacheReadTokens: 30, CacheWriteTokens: 5}); resp.Usage != want {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGenerateRequestBody(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "text.json"))
	req := &model.Request{
		Model: "test-model", System: "be brief", MaxTokens: 100, Effort: "high",
		Messages: []model.Message{userText("hi")},
		Tools: []model.ToolSpec{{
			Name: "fs__read_file", Description: "Read a file.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
		}},
	}
	if _, err := f.adapter().Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b := f.body(t, 0)
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"model", get(t, b, "model"), "test-model"},
		{"max_tokens", get(t, b, "max_tokens"), float64(100)},
		{"system text", get(t, b, "system", 0, "text"), "be brief"},
		{"role", get(t, b, "messages", 0, "role"), "user"},
		{"tool_choice", get(t, b, "tool_choice", "type"), "auto"},
		{"effort", get(t, b, "output_config", "effort"), "high"},
		{"required", get(t, b, "tools", 0, "input_schema", "required"), []any{"path"}},
		{"additionalProperties", get(t, b, "tools", 0, "input_schema", "additionalProperties"), false},
		{"tool name", get(t, b, "tools", 0, "name"), "fs__read_file"},
		{"tool description", get(t, b, "tools", 0, "description"), "Read a file."},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if diff := diff(c.want, c.got); diff != "" {
				t.Errorf("mismatch (-want +got): %s", diff)
			}
		})
	}
}

func TestGenerateNoToolChoiceWithoutTools(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "text.json"))
	if _, err := f.adapter().Generate(context.Background(), simpleReq(userText("hi"))); err != nil {
		t.Fatal(err)
	}
	b := f.body(t, 0)
	for _, k := range []string{"tool_choice", "tools"} {
		if _, ok := b[k]; ok {
			t.Errorf("body has %q", k)
		}
	}
}

// roundTrip runs call 1 (answered by first), then call 2 with call 1's
// message and a tool result, and returns the recorded body of call 2.
func roundTrip(t *testing.T, first string) map[string]any {
	t.Helper()
	f := newFakeAPI(t, nil, first, testdata(t, "text.json"))
	a := f.adapter()
	r1, err := a.Generate(context.Background(), simpleReq(userText("go")))
	if err != nil {
		t.Fatal(err)
	}
	result := model.Message{Role: model.RoleUser, Parts: []model.Part{model.ToolResultPart(model.ToolResult{CallID: "toolu_1", Content: "data"})}}
	if _, err := a.Generate(context.Background(), simpleReq(userText("go"), r1.Message, result)); err != nil {
		t.Fatal(err)
	}
	return f.body(t, 1)
}

func TestThinkingBlobRoundTrip(t *testing.T) {
	b := roundTrip(t, testdata(t, "tool_use.json"))
	want := map[string]any{"type": "thinking", "thinking": "I should read it", "signature": "sig-1"}
	if diff := diff(want, get(t, b, "messages", 1, "content", 0)); diff != "" {
		t.Errorf("thinking block (-want +got): %s", diff)
	}
	if got := get(t, b, "messages", 1, "content", 2, "type"); got != "tool_use" {
		t.Errorf("content[2].type = %v", got)
	}
	if got := get(t, b, "messages", 2, "content", 0, "type"); got != "tool_result" {
		t.Errorf("tool result type = %v", got)
	}
	if got := get(t, b, "messages", 2, "content", 0, "tool_use_id"); got != "toolu_1" {
		t.Errorf("tool_use_id = %v", got)
	}
}

func TestRedactedThinkingRoundTrip(t *testing.T) {
	b := roundTrip(t, testdata(t, "redacted.json"))
	want := map[string]any{"type": "redacted_thinking", "data": "opaque=="}
	if diff := diff(want, get(t, b, "messages", 1, "content", 0)); diff != "" {
		t.Errorf("redacted block (-want +got): %s", diff)
	}
}

func TestForeignBlobsDropped(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "text.json"))
	foreign := model.BlobPart(model.Blob{Provider: "openai", Kind: "reasoning", Data: []byte(`{}`)})
	msgs := []model.Message{
		userText("hi"),
		{Role: model.RoleAssistant, Parts: []model.Part{foreign, model.TextPart("answer")}},
		{Role: model.RoleAssistant, Parts: []model.Part{foreign}},
		userText("more"),
	}
	if _, err := f.adapter().Generate(context.Background(), simpleReq(msgs...)); err != nil {
		t.Fatal(err)
	}
	b := f.body(t, 0)
	all := get(t, b, "messages").([]any)
	if len(all) != 3 {
		t.Fatalf("got %d messages, want 3: %v", len(all), all)
	}
	content := get(t, b, "messages", 1, "content").([]any)
	if len(content) != 1 || get(t, content, 0, "type") != "text" {
		t.Errorf("assistant content = %v", content)
	}
}

func TestToolResultError(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "text.json"))
	msgs := []model.Message{
		userText("hi"),
		{Role: model.RoleAssistant, Parts: []model.Part{model.ToolCallPart(model.ToolCall{ID: "c1", Name: "t"})}},
		{Role: model.RoleUser, Parts: []model.Part{model.ToolResultPart(model.ToolResult{CallID: "c1", Content: "boom", IsError: true})}},
	}
	if _, err := f.adapter().Generate(context.Background(), simpleReq(msgs...)); err != nil {
		t.Fatal(err)
	}
	b := f.body(t, 0)
	if got := get(t, b, "messages", 2, "content", 0, "is_error"); got != true {
		t.Errorf("is_error = %v", got)
	}
	// An empty Arguments value is sent as an empty object.
	if diff := diff(map[string]any{}, get(t, b, "messages", 1, "content", 0, "input")); diff != "" {
		t.Errorf("empty tool input (-want +got): %s", diff)
	}
}

func TestFinishReason(t *testing.T) {
	tests := []struct {
		in   string
		want adlcv1.FinishReason
	}{
		{"end_turn", adlcv1.FinishReason_FINISH_REASON_END_TURN},
		{"stop_sequence", adlcv1.FinishReason_FINISH_REASON_END_TURN},
		{"tool_use", adlcv1.FinishReason_FINISH_REASON_TOOL_USE},
		{"max_tokens", adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS},
		{"model_context_window_exceeded", adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS},
		{"refusal", adlcv1.FinishReason_FINISH_REASON_REFUSAL},
		{"pause_turn", adlcv1.FinishReason_FINISH_REASON_PAUSED},
		{"weird", adlcv1.FinishReason_FINISH_REASON_OTHER},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := finishReason(sdk.StopReason(tt.in)); got != tt.want {
				t.Errorf("finishReason(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		class     adlcv1.ErrorClass
		retryable bool
	}{
		{"429", 429, adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, true},
		{"500", 500, adlcv1.ErrorClass_ERROR_CLASS_MODEL, true},
		{"529", 529, adlcv1.ErrorClass_ERROR_CLASS_MODEL, true},
		{"408", 408, adlcv1.ErrorClass_ERROR_CLASS_MODEL, true},
		{"400", 400, adlcv1.ErrorClass_ERROR_CLASS_MODEL, false},
		{"401", 401, adlcv1.ErrorClass_ERROR_CLASS_MODEL, false},
		{"501", 501, adlcv1.ErrorClass_ERROR_CLASS_MODEL, true}, // interfaces.md §2.1: any 5xx is retryable
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeAPI(t, []int{tt.status}, testdata(t, "error_400.json"))
			_, err := f.adapter().Generate(context.Background(), simpleReq(userText("hi")))
			var me *model.Error
			if !errors.As(err, &me) {
				t.Fatalf("error %v is not *model.Error", err)
			}
			if me.Class != tt.class || me.Retryable != tt.retryable || me.HTTPStatus != tt.status {
				t.Errorf("got {%v %v %d}", me.Class, me.Retryable, me.HTTPStatus)
			}
			if model.IsRetryable(err) != tt.retryable {
				t.Errorf("IsRetryable = %v", model.IsRetryable(err))
			}
		})
	}

	t.Run("closed server", func(t *testing.T) {
		f := newFakeAPI(t, nil, testdata(t, "text.json"))
		a := f.adapter()
		f.srv.Close()
		_, err := a.Generate(context.Background(), simpleReq(userText("hi")))
		var me *model.Error
		if !errors.As(err, &me) {
			t.Fatalf("error %v is not *model.Error", err)
		}
		if me.Class != adlcv1.ErrorClass_ERROR_CLASS_TIMEOUT || !me.Retryable {
			t.Errorf("got {%v %v}", me.Class, me.Retryable)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeAPI(t, nil, testdata(t, "text.json"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := f.adapter().Generate(ctx, simpleReq(userText("hi")))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		var me *model.Error
		if errors.As(err, &me) {
			t.Errorf("cancelled context was wrapped in *model.Error: %v", me)
		}
		if model.IsRetryable(err) {
			t.Error("cancelled context must not be retryable")
		}
	})
}

func TestNoSDKRetries(t *testing.T) {
	f := newFakeAPI(t, []int{500, 500, 500, 500}, testdata(t, "error_400.json"))
	if _, err := f.adapter().Generate(context.Background(), simpleReq(userText("hi"))); err == nil {
		t.Fatal("want error")
	}
	if n := f.requests(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestMaxTokensRequired(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "text.json"))
	req := simpleReq(userText("hi"))
	req.MaxTokens = 0
	_, err := f.adapter().Generate(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "MaxTokens") {
		t.Errorf("error = %v", err)
	}
	if n := f.requests(); n != 0 {
		t.Errorf("%d requests sent, want 0", n)
	}
}

func TestUnknownEffort(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "text.json"))
	req := simpleReq(userText("hi"))
	req.Effort = "extreme"
	_, err := f.adapter().Generate(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "unknown effort") {
		t.Errorf("error = %v", err)
	}
	if n := f.requests(); n != 0 {
		t.Errorf("%d requests sent, want 0", n)
	}
}

func TestCountTokens(t *testing.T) {
	f := newFakeAPI(t, nil, testdata(t, "count.json"))
	req := simpleReq(userText("hi"))
	req.System = "sys"
	req.Tools = []model.ToolSpec{{Name: "t", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	n, err := f.adapter().CountTokens(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Errorf("tokens = %d, want 42", n)
	}
	if f.paths[0] != "POST /v1/messages/count_tokens" {
		t.Errorf("request = %q", f.paths[0])
	}
	b := f.body(t, 0)
	if got := get(t, b, "system", 0, "text"); got != "sys" {
		t.Errorf("system = %v", got)
	}
	if got := get(t, b, "tools", 0, "name"); got != "t" {
		t.Errorf("tool name = %v", got)
	}
}

func TestBuildToolsErrors(t *testing.T) {
	_, err := buildTools([]model.ToolSpec{{Name: "x", InputSchema: json.RawMessage(`{`)}})
	if err == nil || !strings.Contains(err.Error(), "tool x: input schema") {
		t.Errorf("error = %v", err)
	}
}

func TestBuildMessagesUnknownRole(t *testing.T) {
	_, err := buildMessages([]model.Message{{Role: "system", Parts: []model.Part{model.TextPart("x")}}})
	if err == nil || !strings.Contains(err.Error(), "unknown role") {
		t.Errorf("error = %v", err)
	}
}

func TestBlobBlockErrors(t *testing.T) {
	for _, data := range []string{`{`, `{"type":"mystery"}`} {
		if _, err := blobBlock(model.Blob{Provider: "anthropic", Data: []byte(data)}); err == nil {
			t.Errorf("blobBlock(%s) succeeded", data)
		}
	}
}

// contractBody returns the Messages API answer for a contract scenario.
func contractStatusAndBody(sc modeltest.Scenario) (int, string) {
	const msg = `{"id":"m","type":"message","role":"assistant","model":"m","content":%s,"stop_reason":%q,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":%d}}`
	errBody := func(typ string) string {
		return fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":"x"}}`, typ)
	}
	switch sc.Name {
	case modeltest.RateLimited:
		return 429, errBody("rate_limit_error")
	case modeltest.ServerError:
		return 500, errBody("api_error")
	case modeltest.BadRequest:
		return 400, errBody("invalid_request_error")
	}
	w := sc.Want
	stop := map[string]string{
		modeltest.ToolCall: "tool_use", modeltest.ParallelToolCalls: "tool_use",
		modeltest.Refusal: "refusal", modeltest.MaxTokens: "max_tokens",
	}[sc.Name]
	if stop == "" {
		stop = "end_turn"
	}
	var blocks []string
	for _, p := range w.Message.Parts {
		switch p.Kind {
		case model.PartText:
			b, _ := json.Marshal(p.Text)
			blocks = append(blocks, `{"type":"text","text":`+string(b)+`}`)
		case model.PartToolCall:
			blocks = append(blocks, fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s}`, p.ToolCall.ID, p.ToolCall.Name, p.ToolCall.Arguments))
		}
	}
	return 200, fmt.Sprintf(msg, "["+strings.Join(blocks, ",")+"]", stop, w.Usage.InputTokens, w.Usage.OutputTokens)
}

func TestContract(t *testing.T) {
	modeltest.Run(t, func(t *testing.T, sc modeltest.Scenario) model.Adapter {
		status, body := contractStatusAndBody(sc)
		return newFakeAPI(t, []int{status}, body).adapter()
	})
}
