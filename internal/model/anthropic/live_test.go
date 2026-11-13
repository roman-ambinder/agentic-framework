//go:build live

package anthropic

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

func liveAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	modelID := os.Getenv("ADLC_ANTHROPIC_MODEL")
	if os.Getenv("ANTHROPIC_API_KEY") == "" || modelID == "" {
		t.Skip("set ANTHROPIC_API_KEY and ADLC_ANTHROPIC_MODEL")
	}
	return New(Options{}), modelID
}

func TestLiveGenerate(t *testing.T) {
	a, modelID := liveAdapter(t)
	resp, err := a.Generate(context.Background(), &model.Request{
		Model: modelID, MaxTokens: 1024,
		Messages: []model.Message{userText("Reply with exactly the word: pong")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_END_TURN {
		t.Errorf("finish = %v", resp.Finish)
	}
	if !strings.Contains(strings.ToLower(resp.Message.Text()), "pong") {
		t.Errorf("text = %q", resp.Message.Text())
	}
	if resp.Usage.InputTokens <= 0 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestLiveToolCall(t *testing.T) {
	a, modelID := liveAdapter(t)
	tools := []model.ToolSpec{{
		Name: "get_weather", Description: "Get the weather for a city.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
	}}
	msgs := []model.Message{userText("What is the weather in Paris? Use the tool.")}
	resp, err := a.Generate(context.Background(), &model.Request{Model: modelID, MaxTokens: 1024, Messages: msgs, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	var call *model.ToolCall
	for _, c := range resp.Message.ToolCalls() {
		if c.Name == "get_weather" {
			call = &c
			break
		}
	}
	if call == nil || !strings.Contains(string(call.Arguments), "Paris") {
		t.Fatalf("no get_weather call for Paris in %+v", resp.Message)
	}
	msgs = append(msgs, resp.Message, model.Message{Role: model.RoleUser, Parts: []model.Part{
		model.ToolResultPart(model.ToolResult{CallID: call.ID, Content: "sunny"}),
	}})
	resp2, err := a.Generate(context.Background(), &model.Request{Model: modelID, MaxTokens: 1024, Messages: msgs, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if resp2.Finish != adlcv1.FinishReason_FINISH_REASON_END_TURN {
		t.Errorf("finish = %v", resp2.Finish)
	}
}

func TestLiveCacheHit(t *testing.T) {
	a, modelID := liveAdapter(t)
	para := "The quick brown fox jumps over the lazy dog while the build pipeline runs every test twice. "
	req := &model.Request{
		Model: modelID, MaxTokens: 64,
		System:   "You are a terse assistant. Background notes: " + strings.Repeat(para, 300),
		Messages: []model.Message{userText("Reply with exactly the word: pong")},
	}
	if _, err := a.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	resp, err := a.Generate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.CacheReadTokens <= 0 {
		t.Errorf("second call usage = %+v, want CacheReadTokens > 0", resp.Usage)
	}
}
