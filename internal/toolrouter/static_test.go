package toolrouter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

func echoTool(name string) StaticTool {
	return StaticTool{
		Tool: Tool{
			Spec:   model.ToolSpec{Name: name, InputSchema: []byte(`{"type":"object"}`)},
			Server: "test",
			Risk:   adlcv1.RiskClass_RISK_CLASS_READ,
		},
		Fn: func(_ context.Context, args json.RawMessage) (Result, error) {
			return Result{Output: args}, nil
		},
	}
}

func mustStatic(t *testing.T, tools ...StaticTool) *Static {
	t.Helper()
	s, err := NewStatic(tools...)
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}
	return s
}

func TestStaticToolsInOrder(t *testing.T) {
	s := mustStatic(t, echoTool("test__b"), echoTool("test__a"), echoTool("test__c"))
	var names []string
	for _, tl := range s.Tools() {
		names = append(names, tl.Spec.Name)
	}
	want := "test__b,test__a,test__c"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("Tools names = %s, want %s", got, want)
	}
}

func TestStaticToolsReturnsCopy(t *testing.T) {
	s := mustStatic(t, echoTool("test__a"), echoTool("test__b"))
	first := s.Tools()
	first[0].Spec.Name = "changed"
	first[1] = Tool{}
	second := s.Tools()
	if second[0].Spec.Name != "test__a" || second[1].Spec.Name != "test__b" {
		t.Fatalf("second Tools() changed after editing first: %+v", second)
	}
	second[0].Spec.InputSchema[0] = 'X'
	if third := s.Tools(); string(third[0].Spec.InputSchema) != `{"type":"object"}` {
		t.Fatalf("schema bytes shared with caller: %s", third[0].Spec.InputSchema)
	}
}

func TestStaticCallRunsFunction(t *testing.T) {
	s := mustStatic(t, echoTool("test__echo"))
	res, err := s.Call(context.Background(), model.ToolCall{
		ID: "1", Name: "test__echo", Arguments: json.RawMessage(`{"a":1}`),
	})
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if string(res.Output) != `{"a":1}` {
		t.Fatalf("Output = %s, want {\"a\":1}", res.Output)
	}
}

func TestStaticCallEmptyArguments(t *testing.T) {
	s := mustStatic(t, echoTool("test__echo"))
	res, err := s.Call(context.Background(), model.ToolCall{Name: "test__echo"})
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if string(res.Output) != "{}" {
		t.Fatalf("Output = %s, want {}", res.Output)
	}
}

func TestStaticCallUnknownTool(t *testing.T) {
	s := mustStatic(t, echoTool("test__echo"))
	_, err := s.Call(context.Background(), model.ToolCall{Name: "test__nope"})
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("error = %v, want ErrUnknownTool", err)
	}
}

func TestStaticCallToolError(t *testing.T) {
	tl := echoTool("test__fail")
	tl.Fn = func(context.Context, json.RawMessage) (Result, error) {
		return Result{IsError: true, Output: []byte("bad")}, nil
	}
	s := mustStatic(t, tl)
	res, err := s.Call(context.Background(), model.ToolCall{Name: "test__fail"})
	if err != nil {
		t.Fatalf("Call error = %v, want nil", err)
	}
	if !res.IsError || string(res.Output) != "bad" {
		t.Fatalf("Result = %+v, want IsError with output bad", res)
	}
}

func TestStaticCallFuncError(t *testing.T) {
	sentinel := errors.New("server crashed")
	tl := echoTool("test__crash")
	tl.Fn = func(context.Context, json.RawMessage) (Result, error) {
		return Result{}, sentinel
	}
	s := mustStatic(t, tl)
	_, err := s.Call(context.Background(), model.ToolCall{Name: "test__crash"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the function error", err)
	}
	if want := "toolrouter: test__crash: server crashed"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestStaticCallCancelledContext(t *testing.T) {
	called := false
	tl := echoTool("test__echo")
	tl.Fn = func(context.Context, json.RawMessage) (Result, error) {
		called = true
		return Result{}, nil
	}
	s := mustStatic(t, tl)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Call(ctx, model.ToolCall{Name: "test__echo"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("Fn was called with a cancelled context")
	}
}

func TestStaticCallSetsDuration(t *testing.T) {
	tl := echoTool("test__slow")
	tl.Fn = func(context.Context, json.RawMessage) (Result, error) {
		time.Sleep(5 * time.Millisecond)
		return Result{}, nil
	}
	s := mustStatic(t, tl)
	res, err := s.Call(context.Background(), model.ToolCall{Name: "test__slow"})
	if err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if res.Duration < 5*time.Millisecond {
		t.Fatalf("Duration = %v, want >= 5ms", res.Duration)
	}
}

func TestNewStaticRejects(t *testing.T) {
	noFn := echoTool("test__nofn")
	noFn.Fn = nil
	invalid := echoTool("test__ok")
	invalid.Tool.Risk = adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED
	tests := []struct {
		name    string
		tools   []StaticTool
		wantErr string
	}{
		{
			name: "duplicate name", tools: []StaticTool{echoTool("test__a"), echoTool("test__a")},
			wantErr: `toolrouter: duplicate tool "test__a"`,
		},
		{
			name: "nil function", tools: []StaticTool{noFn},
			wantErr: `toolrouter: tool "test__nofn" has no function`,
		},
		{
			name: "invalid tool", tools: []StaticTool{invalid},
			wantErr: `toolrouter: tool "test__ok" has no risk class`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewStatic(tt.tools...)
			if err == nil {
				t.Fatalf("NewStatic error = nil, want %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("NewStatic error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}
