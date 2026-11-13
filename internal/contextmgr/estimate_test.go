package contextmgr

import (
	"encoding/json"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{{"", 0}, {"abcd", 1}, {"abcde", 2}}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := EstimateTokens(tt.in); got != tt.want {
				t.Errorf("EstimateTokens(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestEstimateRequestCountsEverything(t *testing.T) {
	base := transcript(1, 40)
	baseEst := EstimateRequest(base)
	tests := []struct {
		name string
		edit func(r *model.Request)
	}{
		{"system", func(r *model.Request) { r.System = "be careful" }},
		{"tool spec", func(r *model.Request) {
			r.Tools = []model.ToolSpec{{Name: "fs__read", Description: "reads", InputSchema: json.RawMessage(`{"type":"object"}`)}}
		}},
		{"tool call", func(r *model.Request) {
			r.Messages[1].Parts = append(r.Messages[1].Parts,
				model.ToolCallPart(model.ToolCall{ID: "x", Name: "fs__write", Arguments: json.RawMessage(`{"a":"bbbbbbbb"}`)}))
		}},
		{"tool result", func(r *model.Request) {
			r.Messages[2].Parts = append(r.Messages[2].Parts, model.ToolResultPart(model.ToolResult{CallID: "x", Content: "zzzzzzzz"}))
		}},
		{"blob", func(r *model.Request) {
			r.Messages[1].Parts = append(r.Messages[1].Parts, model.BlobPart(model.Blob{Provider: "p", Kind: "thinking", Data: []byte("12345678")}))
		}},
		{"text", func(r *model.Request) {
			r.Messages[0].Parts = append(r.Messages[0].Parts, model.TextPart("more text here"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := deepCopy(base)
			tt.edit(r)
			if got := EstimateRequest(r); got <= baseEst {
				t.Errorf("estimate %d did not grow from %d", got, baseEst)
			}
		})
	}
}
