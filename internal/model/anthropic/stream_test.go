package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// sseServer answers every POST with testdata/<fixture> as a 200 event
// stream and stores the last request body in *capture (if not nil).
func sseServer(t *testing.T, fixture string, capture *[]byte) *httptest.Server {
	t.Helper()
	data, err := os.ReadFile("testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if capture != nil {
			*capture = b
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func streamAdapter(t *testing.T, fixture string, capture *[]byte, opts Options) *Adapter {
	t.Helper()
	srv := sseServer(t, fixture, capture)
	opts.APIKey, opts.BaseURL = "test-key", srv.URL
	return New(opts)
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode request body %q: %v", b, err)
	}
	return m
}

func TestGenerateStreamsToolUse(t *testing.T) {
	a := streamAdapter(t, "stream_tool_use.sse", nil, Options{})
	resp, err := a.Generate(context.Background(), simpleReq(userText("hi")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_TOOL_USE || resp.ProviderFinish != "tool_use" {
		t.Errorf("finish = %v %q", resp.Finish, resp.ProviderFinish)
	}
	if got := resp.Message.Text(); got != "Reading the file." {
		t.Errorf("text = %q", got)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "toolu_1" || calls[0].Name != "fs__read_file" {
		t.Fatalf("calls = %+v", calls)
	}
	var args map[string]any
	if err := json.Unmarshal(calls[0].Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if d := diff(map[string]any{"path": "main.go"}, args); d != "" {
		t.Errorf("arguments: %s", d)
	}
	want := model.Usage{InputTokens: 12, OutputTokens: 25, CacheReadTokens: 100, CacheWriteTokens: 20}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestGenerateSendsStreamTrue(t *testing.T) {
	var body []byte
	a := streamAdapter(t, "stream_tool_use.sse", &body, Options{})
	if _, err := a.Generate(context.Background(), simpleReq(userText("hi"))); err != nil {
		t.Fatal(err)
	}
	if got := decode(t, body)["stream"]; got != true {
		t.Errorf("stream = %v", got)
	}
}

func TestGenerateSetsCacheControl(t *testing.T) {
	var body []byte
	a := streamAdapter(t, "stream_tool_use.sse", &body, Options{})
	req := simpleReq(userText("hi"))
	req.System = "sys"
	if _, err := a.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b := decode(t, body)
	if got := get(t, b, "system", 0, "cache_control", "type"); got != "ephemeral" {
		t.Errorf("system cache_control = %v", got)
	}
	if got := get(t, b, "cache_control", "type"); got != "ephemeral" {
		t.Errorf("top-level cache_control = %v", got)
	}
}

func TestGenerateCachingDisabled(t *testing.T) {
	var body []byte
	a := streamAdapter(t, "stream_tool_use.sse", &body, Options{DisableCaching: true})
	req := simpleReq(userText("hi"))
	req.System = "sys"
	if _, err := a.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	b := decode(t, body)
	if _, ok := b["cache_control"]; ok {
		t.Error("body has top-level cache_control")
	}
	sys, _ := get(t, b, "system", 0).(map[string]any)
	if _, ok := sys["cache_control"]; ok {
		t.Error("system[0] has cache_control")
	}
}

func TestCountTokensEndpoint(t *testing.T) {
	f := newFakeAPI(t, nil, `{"input_tokens": 1234}`)
	req := simpleReq(userText("hi"))
	req.System = "sys prompt"
	req.Tools = []model.ToolSpec{{Name: "fs__read_file", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	n, err := f.adapter().CountTokens(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1234 {
		t.Errorf("tokens = %d", n)
	}
	if f.paths[0] != "POST /v1/messages/count_tokens" {
		t.Errorf("request = %q", f.paths[0])
	}
	b := f.body(t, 0)
	if got := get(t, b, "system", 0, "text"); got != "sys prompt" {
		t.Errorf("system = %v", got)
	}
	if got := get(t, b, "tools", 0, "name"); got != "fs__read_file" {
		t.Errorf("tool name = %v", got)
	}
	if _, ok := b["cache_control"]; ok {
		t.Error("count request must not carry cache_control")
	}
}

func TestCountTokensWithoutMaxTokens(t *testing.T) {
	f := newFakeAPI(t, nil, `{"input_tokens": 7}`)
	req := simpleReq(userText("hi"))
	req.MaxTokens = 0
	n, err := f.adapter().CountTokens(context.Background(), req)
	if err != nil || n != 7 {
		t.Errorf("got %d, %v", n, err)
	}
}

func TestStreamErrorMidway(t *testing.T) {
	a := streamAdapter(t, "stream_error.sse", nil, Options{})
	_, err := a.Generate(context.Background(), simpleReq(userText("hi")))
	if err == nil {
		t.Fatal("want error")
	}
	t.Logf("%T %v", err, err)
	if !model.IsRetryable(err) {
		t.Errorf("error %v is not retryable", err)
	}
}

func TestStreamEndsEarly(t *testing.T) {
	a := streamAdapter(t, "stream_early_end.sse", nil, Options{})
	_, err := a.Generate(context.Background(), simpleReq(userText("hi")))
	if err == nil || !strings.Contains(err.Error(), "stream ended before the message was complete") {
		t.Fatalf("error = %v", err)
	}
	if !model.IsRetryable(err) {
		t.Error("want retryable")
	}
}

func TestCapabilities(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want model.Caps
	}{
		{"default", Options{MaxContextTokens: 321}, model.Caps{ToolCalling: true, PromptCaching: true, Streaming: true, MaxContextTokens: 321}},
		{"caching disabled", Options{DisableCaching: true}, model.Caps{ToolCalling: true, Streaming: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.APIKey = "test-key"
			if got := New(tt.opts).Capabilities(); got != tt.want {
				t.Errorf("Capabilities = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestClassifyStreamError(t *testing.T) {
	tests := []struct {
		typ       string
		class     adlcv1.ErrorClass
		retryable bool
	}{
		{"overloaded_error", adlcv1.ErrorClass_ERROR_CLASS_MODEL, true},
		{"api_error", adlcv1.ErrorClass_ERROR_CLASS_MODEL, true},
		{"rate_limit_error", adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, true},
		{"invalid_request_error", adlcv1.ErrorClass_ERROR_CLASS_MODEL, false},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			data := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"" + tt.typ + "\",\"message\":\"x\"}}\n\n"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, data)
			}))
			defer srv.Close()
			_, err := New(Options{APIKey: "k", BaseURL: srv.URL}).Generate(context.Background(), simpleReq(userText("hi")))
			var me *model.Error
			if !errors.As(err, &me) {
				t.Fatalf("error %v is not *model.Error", err)
			}
			if me.Class != tt.class || me.Retryable != tt.retryable {
				t.Errorf("got {%v %v}", me.Class, me.Retryable)
			}
		})
	}
}
