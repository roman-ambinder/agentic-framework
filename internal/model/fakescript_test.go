package model

import (
	"errors"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

func TestLoadFakeScriptExample(t *testing.T) {
	turns, err := LoadFakeScript("testdata/hello.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 {
		t.Fatalf("got %d turns", len(turns))
	}
	r0 := turns[0].Response
	calls := r0.Message.ToolCalls()
	if r0.Message.Text() != "Let me read the file." || len(calls) != 1 ||
		string(calls[0].Arguments) != `{"path": "README.md"}` && string(calls[0].Arguments) != `{"path":"README.md"}` ||
		r0.Finish != adlcv1.FinishReason_FINISH_REASON_TOOL_USE || r0.Usage.InputTokens != 120 {
		t.Fatalf("turn 0 = %+v", r0)
	}
	var me *Error
	if !errors.As(turns[1].Err, &me) || me.Class != adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT ||
		me.HTTPStatus != 429 || !me.Retryable || me.Err.Error() != "slow down" {
		t.Fatalf("turn 1 = %v", turns[1].Err)
	}
	if turns[2].Response.Usage.CacheReadTokens != 100 {
		t.Fatalf("turn 2 = %+v", turns[2].Response)
	}
}

func TestParseFakeScriptErrors(t *testing.T) {
	tests := []struct {
		name, json, want string
	}{
		{"empty turns", `{"turns":[]}`, "no turns"},
		{"unknown field", `{"turns":[{"text":"a"},{"text":"b","bogus":1}]}`, "turn 1"},
		{"error and text", `{"turns":[{"text":"a"},{"text":"a"},{"text":"b","error":{"class":"model"}}]}`, "turn 2"},
		{"nothing", `{"turns":[{"text":"a"},{}]}`, "turn 1"},
		{"bad finish", `{"turns":[{"text":"a","finish":"bogus"}]}`, "turn 0"},
		{"bad tool name", `{"turns":[{"tool_calls":[{"id":"1","name":"fs.read"}]}]}`, "turn 0"},
		{"arguments not object", `{"turns":[{"text":"a"},{"tool_calls":[{"id":"1","name":"a","arguments":[1]}]}]}`, "turn 1"},
		{"bad error class", `{"turns":[{"error":{"class":"bogus"}}]}`, "turn 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseFakeScript([]byte(tt.json))
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestParseFakeScriptDefaultFinish(t *testing.T) {
	turns, err := ParseFakeScript([]byte(`{"turns":[
		{"tool_calls":[{"id":"1","name":"a"}]},
		{"text":"hi"},
		{"finish":"refusal"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []adlcv1.FinishReason{
		adlcv1.FinishReason_FINISH_REASON_TOOL_USE,
		adlcv1.FinishReason_FINISH_REASON_END_TURN,
		adlcv1.FinishReason_FINISH_REASON_REFUSAL,
	}
	for i, w := range want {
		if got := turns[i].Response.Finish; got != w {
			t.Fatalf("turn %d finish = %v, want %v", i, got, w)
		}
	}
	if string(turns[0].Response.Message.ToolCalls()[0].Arguments) != "{}" {
		t.Fatal("default arguments not {}")
	}
}
