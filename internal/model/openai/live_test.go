//go:build live

package openai

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

func TestLiveToolCall(t *testing.T) {
	modelID := os.Getenv("ADLC_OPENAI_MODEL")
	if os.Getenv("OPENAI_API_KEY") == "" || modelID == "" {
		t.Skip("set OPENAI_API_KEY and ADLC_OPENAI_MODEL")
	}
	a, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Generate(context.Background(), &model.Request{
		Model: modelID, MaxTokens: 1024,
		Messages: []model.Message{userText("What time is it? Call the get_time tool.")},
		Tools: []model.ToolSpec{{
			Name: "get_time", Description: "Get the current time.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.ToolCalls()
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_TOOL_USE || len(calls) != 1 || calls[0].Name != "get_time" {
		t.Fatalf("finish = %v, calls = %+v", resp.Finish, calls)
	}
}
