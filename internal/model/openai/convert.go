package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// buildParams maps a model.Request to Chat Completions parameters.
func buildParams(req *model.Request) (oai.ChatCompletionNewParams, error) {
	p := oai.ChatCompletionNewParams{Model: req.Model}
	if req.System != "" {
		p.Messages = append(p.Messages, oai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		switch m.Role {
		case model.RoleUser:
			p.Messages = append(p.Messages, userMessages(m)...)
		case model.RoleAssistant:
			if a, ok := assistantMessage(m); ok {
				p.Messages = append(p.Messages, a)
			}
		}
	}
	if err := addTools(&p, req.Tools); err != nil {
		return p, err
	}
	if req.MaxTokens > 0 {
		p.MaxCompletionTokens = oai.Int(int64(req.MaxTokens))
	}
	if req.Effort != "" {
		p.ReasoningEffort = shared.ReasoningEffort(req.Effort)
	}
	return p, nil
}

// userMessages returns the tool messages of m (first, because the API wants
// them right after the assistant's tool calls) and then one user message with
// the text parts, if any.
func userMessages(m model.Message) []oai.ChatCompletionMessageParamUnion {
	var out []oai.ChatCompletionMessageParamUnion
	var texts []string
	for _, part := range m.Parts {
		switch part.Kind {
		case model.PartToolResult:
			r := part.ToolResult
			content := r.Content
			if r.IsError {
				content = "error: " + content
			}
			out = append(out, oai.ToolMessage(content, r.CallID))
		case model.PartText:
			texts = append(texts, part.Text)
		}
	}
	if text := strings.Join(texts, "\n"); text != "" {
		out = append(out, oai.UserMessage(text))
	}
	return out
}

// assistantMessage maps an assistant message; blobs are dropped.
func assistantMessage(m model.Message) (oai.ChatCompletionMessageParamUnion, bool) {
	text := m.Text()
	calls := m.ToolCalls()
	if text == "" && len(calls) == 0 {
		return oai.ChatCompletionMessageParamUnion{}, false
	}
	a := oai.ChatCompletionAssistantMessageParam{}
	if text != "" {
		a.Content.OfString = oai.String(text)
	}
	for _, c := range calls {
		a.ToolCalls = append(a.ToolCalls, oai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &oai.ChatCompletionMessageFunctionToolCallParam{
				ID: c.ID,
				Function: oai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name: c.Name, Arguments: string(c.Arguments),
				},
			},
		})
	}
	return oai.ChatCompletionMessageParamUnion{OfAssistant: &a}, true
}

func addTools(p *oai.ChatCompletionNewParams, tools []model.ToolSpec) error {
	for _, t := range tools {
		var schema map[string]any
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
			return fmt.Errorf("openai: tool %q: input schema is not a JSON object: %w", t.Name, err)
		}
		p.Tools = append(p.Tools, oai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: oai.String(t.Description),
			Parameters:  shared.FunctionParameters(schema),
		}))
	}
	if len(tools) > 0 {
		p.ToolChoice.OfAuto = oai.String("auto")
	}
	return nil
}

// toResponse maps a Chat Completions answer to a model.Response.
func toResponse(c *oai.ChatCompletion) (*model.Response, error) {
	if len(c.Choices) == 0 {
		return nil, &model.Error{
			Class:     adlcv1.ErrorClass_ERROR_CLASS_MODEL,
			Retryable: true,
			Err:       errors.New("openai: response has no choices"),
		}
	}
	ch := c.Choices[0]
	msg := ch.Message
	finish := finishReason(ch.FinishReason)
	text := msg.Content
	if msg.Refusal != "" {
		finish = adlcv1.FinishReason_FINISH_REASON_REFUSAL
		text = msg.Refusal
	}
	out := model.Message{Role: model.RoleAssistant}
	if text != "" {
		out.Parts = append(out.Parts, model.TextPart(text))
	}
	for _, tc := range msg.ToolCalls {
		if tc.Type != "function" {
			continue
		}
		f := tc.AsFunction()
		out.Parts = append(out.Parts, model.ToolCallPart(model.ToolCall{
			ID: f.ID, Name: f.Function.Name, Arguments: args(f.Function.Arguments),
		}))
	}
	return &model.Response{
		Message:        out,
		Finish:         finish,
		ProviderFinish: ch.FinishReason,
		Usage:          usage(c.Usage),
	}, nil
}

// args returns s as raw JSON; text that is not valid JSON becomes a JSON
// string so the tool rejects it and the model sees an error it can correct.
func args(s string) json.RawMessage {
	switch {
	case s == "":
		return json.RawMessage(`{}`)
	case json.Valid([]byte(s)):
		return json.RawMessage(s)
	default:
		return json.RawMessage(strconv.Quote(s))
	}
}

func finishReason(s string) adlcv1.FinishReason {
	switch s {
	case "stop":
		return adlcv1.FinishReason_FINISH_REASON_END_TURN
	case "tool_calls":
		return adlcv1.FinishReason_FINISH_REASON_TOOL_USE
	case "length":
		return adlcv1.FinishReason_FINISH_REASON_MAX_TOKENS
	case "content_filter":
		return adlcv1.FinishReason_FINISH_REASON_REFUSAL
	default:
		return adlcv1.FinishReason_FINISH_REASON_OTHER
	}
}

// usage subtracts cached tokens from the prompt tokens: OpenAI counts them
// inside prompt_tokens, the framework counts them separately.
func usage(u oai.CompletionUsage) model.Usage {
	cached := u.PromptTokensDetails.CachedTokens
	return model.Usage{
		InputTokens:      max(u.PromptTokens-cached, 0),
		CacheReadTokens:  cached,
		CacheWriteTokens: u.PromptTokensDetails.CacheWriteTokens,
		OutputTokens:     u.CompletionTokens,
	}
}
