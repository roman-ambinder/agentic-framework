package anthropic

import (
	"encoding/json"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// toResponse converts an API message to a neutral response. Parts keep the
// order of the API's content blocks so thinking blocks can be replayed.
func toResponse(msg *sdk.Message) *model.Response {
	var parts []model.Part
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			parts = append(parts, model.TextPart(block.Text))
		case "tool_use":
			args := append(json.RawMessage(nil), block.Input...)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			parts = append(parts, model.ToolCallPart(model.ToolCall{ID: block.ID, Name: block.Name, Arguments: args}))
		case "thinking", "redacted_thinking":
			parts = append(parts, model.BlobPart(model.Blob{Provider: ProviderName, Kind: block.Type, Data: []byte(block.RawJSON())}))
		}
	}
	u := msg.Usage
	return &model.Response{
		Message:        model.Message{Role: model.RoleAssistant, Parts: parts},
		Finish:         finishReason(msg.StopReason),
		ProviderFinish: string(msg.StopReason),
		Usage: model.Usage{
			InputTokens:      u.InputTokens,
			OutputTokens:     u.OutputTokens,
			CacheReadTokens:  u.CacheReadInputTokens,
			CacheWriteTokens: u.CacheCreationInputTokens,
		},
	}
}

// finishReason maps an Anthropic stop reason to the neutral finish reason.
func finishReason(r sdk.StopReason) adlcv1.FinishReason {
	switch r {
	case "end_turn", "stop_sequence":
		return adlcv1.FinishReason_FINISH_REASON_END_TURN
	case "tool_use":
		return adlcv1.FinishReason_FINISH_REASON_TOOL_USE
	case "max_tokens", "model_context_window_exceeded":
		return adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS
	case "refusal":
		return adlcv1.FinishReason_FINISH_REASON_REFUSAL
	case "pause_turn":
		return adlcv1.FinishReason_FINISH_REASON_PAUSED
	default:
		return adlcv1.FinishReason_FINISH_REASON_OTHER
	}
}
