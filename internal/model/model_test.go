package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestMessageText(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{"two text parts", Message{Parts: []Part{TextPart("a"), TextPart("b")}}, "a\nb"},
		{"ignores tool calls", Message{Parts: []Part{TextPart("a"), ToolCallPart(ToolCall{ID: "1"}), TextPart("b")}}, "a\nb"},
		{"empty", Message{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.Text(); got != tt.want {
				t.Fatalf("Text() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMessageToolCalls(t *testing.T) {
	c1 := ToolCall{ID: "1", Name: "a", Arguments: json.RawMessage(`{}`)}
	c2 := ToolCall{ID: "2", Name: "b", Arguments: json.RawMessage(`{}`)}
	m := Message{Parts: []Part{ToolCallPart(c1), TextPart("x"), ToolCallPart(c2)}}
	if got := m.ToolCalls(); !reflect.DeepEqual(got, []ToolCall{c1, c2}) {
		t.Fatalf("ToolCalls() = %v", got)
	}
	if got := (Message{Parts: []Part{TextPart("x")}}).ToolCalls(); got != nil {
		t.Fatalf("ToolCalls() = %v, want nil", got)
	}
}

func TestPartHelpersCopy(t *testing.T) {
	c := ToolCall{ID: "1", Name: "before"}
	p := ToolCallPart(c)
	c.Name = "after"
	if p.ToolCall.Name != "before" {
		t.Fatalf("part changed: %q", p.ToolCall.Name)
	}
	r := ToolResult{CallID: "1", Content: "x"}
	rp := ToolResultPart(r)
	r.Content = "y"
	if rp.ToolResult.Content != "x" || rp.Kind != PartToolResult {
		t.Fatalf("bad result part %+v", rp)
	}
	b := Blob{Kind: "k"}
	if bp := BlobPart(b); bp.Kind != PartBlob || bp.Blob.Kind != "k" {
		t.Fatalf("bad blob part %+v", bp)
	}
}
