package model

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func textTurn(s string) FakeTurn {
	return FakeTurn{Response: &Response{Message: Message{Role: RoleAssistant, Parts: []Part{TextPart(s)}}}}
}

func TestFakeReturnsTurnsInOrder(t *testing.T) {
	f := NewFake(textTurn("one"), textTurn("two"))
	ctx := context.Background()
	for _, want := range []string{"one", "two"} {
		resp, err := f.Generate(ctx, &Request{})
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Message.Text(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
	if _, err := f.Generate(ctx, &Request{}); !errors.Is(err, ErrScriptExhausted) {
		t.Fatalf("err = %v, want ErrScriptExhausted", err)
	}
}

func TestFakeReturnsScriptedError(t *testing.T) {
	want := &Error{Retryable: true, Err: errors.New("x")}
	f := NewFake(FakeTurn{Err: want})
	_, err := f.Generate(context.Background(), &Request{})
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestFakeRecordsDeepCopies(t *testing.T) {
	f := NewFake(textTurn("x"))
	req := &Request{
		System: "sys",
		Messages: []Message{{Role: RoleAssistant, Parts: []Part{
			ToolCallPart(ToolCall{ID: "1", Name: "a", Arguments: json.RawMessage(`{"k":1}`)}),
		}}},
		Tools: []ToolSpec{{Name: "a", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	if _, err := f.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.System = "changed"
	req.Messages[0].Parts[0].ToolCall.Name = "changed"
	req.Messages[0].Parts[0].ToolCall.Arguments[2] = 'z'
	req.Messages[0].Parts[0] = TextPart("changed")
	req.Tools[0].Name = "changed"
	req.Tools[0].InputSchema[2] = 'z'
	got := f.Requests()[0]
	call := got.Messages[0].Parts[0].ToolCall
	if got.System != "sys" || call == nil || call.Name != "a" || string(call.Arguments) != `{"k":1}` ||
		got.Tools[0].Name != "a" || string(got.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("recorded request was modified: %+v", got)
	}
}

func TestFakeResponseIsCopy(t *testing.T) {
	turn := FakeTurn{Response: &Response{Message: Message{Role: RoleAssistant, Parts: []Part{
		ToolCallPart(ToolCall{ID: "1", Name: "a", Arguments: json.RawMessage(`{"k":1}`)}),
	}}}}
	f := NewFake(turn, turn)
	ctx := context.Background()
	r1, _ := f.Generate(ctx, &Request{})
	r1.Message.Parts[0].ToolCall.Name = "changed"
	r1.Message.Parts[0].ToolCall.Arguments[2] = 'z'
	r2, _ := f.Generate(ctx, &Request{})
	call := r2.Message.Parts[0].ToolCall
	if call.Name != "a" || string(call.Arguments) != `{"k":1}` {
		t.Fatalf("second response was modified: %+v", call)
	}
}

func TestFakeHonoursCancelledContext(t *testing.T) {
	f := NewFake(textTurn("x"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Generate(ctx, &Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if f.Remaining() != 1 || len(f.Requests()) != 0 {
		t.Fatalf("remaining %d, requests %d", f.Remaining(), len(f.Requests()))
	}
}

func TestFakeConcurrentUse(t *testing.T) {
	turns := make([]FakeTurn, 10)
	for i := range turns {
		turns[i] = textTurn("x")
	}
	f := NewFake(turns...)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.Generate(context.Background(), &Request{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.Remaining() != 0 || len(f.Requests()) != 10 {
		t.Fatalf("remaining %d, requests %d", f.Remaining(), len(f.Requests()))
	}
}

func TestFakeCountTokens(t *testing.T) {
	f := NewFake(textTurn("x"))
	req := &Request{
		System: "12345", // 5
		Messages: []Message{
			{Role: RoleUser, Parts: []Part{TextPart("abc"), ToolResultPart(ToolResult{Content: "12"})}},         // 3 + 2
			{Role: RoleAssistant, Parts: []Part{ToolCallPart(ToolCall{Arguments: json.RawMessage(`{"a":1}`)})}}, // 7
		},
		Tools: []ToolSpec{{Name: "n", Description: "dd", InputSchema: json.RawMessage(`{}`)}}, // 1+2+2
	}
	// total 22 -> ceil(22/4) = 6
	got, err := f.CountTokens(context.Background(), req)
	if err != nil || got != 6 {
		t.Fatalf("got %d, %v; want 6", got, err)
	}
	if f.Remaining() != 1 {
		t.Fatal("CountTokens consumed a turn")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.CountTokens(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeSetsProviderAndModel(t *testing.T) {
	f := NewFake(textTurn("one"))
	resp, err := f.Generate(context.Background(), &Request{Model: "fake-x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "fake" || resp.Model != "fake-x" {
		t.Errorf("provider/model = %q/%q, want fake/fake-x", resp.Provider, resp.Model)
	}
}
