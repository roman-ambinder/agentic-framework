package anthropic

import (
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// buildParams builds the request parameters shared by Generate and CountTokens.
func (a *Adapter) buildParams(req *model.Request) (sdk.MessageNewParams, error) {
	msgs, err := buildMessages(req.Messages)
	if err != nil {
		return sdk.MessageNewParams{}, err
	}
	tools, err := buildTools(req.Tools)
	if err != nil {
		return sdk.MessageNewParams{}, err
	}
	eff, err := effort(req.Effort)
	if err != nil {
		return sdk.MessageNewParams{}, err
	}
	p := sdk.MessageNewParams{Model: req.Model, MaxTokens: int64(req.MaxTokens), Messages: msgs}
	if req.System != "" {
		p.System = []sdk.TextBlockParam{{Text: req.System}}
	}
	if len(tools) > 0 {
		p.Tools = tools
		p.ToolChoice = sdk.ToolChoiceUnionParam{OfAuto: &sdk.ToolChoiceAutoParam{}}
	}
	if eff != "" {
		p.OutputConfig = sdk.OutputConfigParam{Effort: eff}
	}
	return p, nil
}

// buildMessages converts neutral messages to API messages. Blobs of other
// providers are dropped; messages left empty are skipped.
func buildMessages(msgs []model.Message) ([]sdk.MessageParam, error) {
	var out []sdk.MessageParam
	for _, m := range msgs {
		blocks, err := buildBlocks(m.Parts)
		if err != nil {
			return nil, err
		}
		if len(blocks) == 0 {
			continue
		}
		switch m.Role {
		case model.RoleUser:
			out = append(out, sdk.NewUserMessage(blocks...))
		case model.RoleAssistant:
			out = append(out, sdk.NewAssistantMessage(blocks...))
		default:
			return nil, fmt.Errorf("anthropic: unknown role %q", m.Role)
		}
	}
	return out, nil
}

func buildBlocks(parts []model.Part) ([]sdk.ContentBlockParamUnion, error) {
	var blocks []sdk.ContentBlockParamUnion
	for _, p := range parts {
		switch p.Kind {
		case model.PartText:
			blocks = append(blocks, sdk.NewTextBlock(p.Text))
		case model.PartToolCall:
			if p.ToolCall == nil {
				return nil, fmt.Errorf("anthropic: tool call part without ToolCall")
			}
			var args any = p.ToolCall.Arguments
			if len(p.ToolCall.Arguments) == 0 {
				args = json.RawMessage("{}")
			}
			blocks = append(blocks, sdk.NewToolUseBlock(p.ToolCall.ID, args, p.ToolCall.Name))
		case model.PartToolResult:
			if p.ToolResult == nil {
				return nil, fmt.Errorf("anthropic: tool result part without ToolResult")
			}
			r := p.ToolResult
			blocks = append(blocks, sdk.NewToolResultBlock(r.CallID, r.Content, r.IsError))
		case model.PartBlob:
			if p.Blob == nil || p.Blob.Provider != ProviderName {
				continue
			}
			b, err := blobBlock(*p.Blob)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, b)
		}
	}
	return blocks, nil
}

// blobBlock rebuilds a thinking block from the JSON stored by toResponse.
func blobBlock(b model.Blob) (sdk.ContentBlockParamUnion, error) {
	var raw struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(b.Data, &raw); err != nil {
		return sdk.ContentBlockParamUnion{}, fmt.Errorf("anthropic: bad blob: %w", err)
	}
	switch raw.Type {
	case "thinking":
		return sdk.NewThinkingBlock(raw.Signature, raw.Thinking), nil
	case "redacted_thinking":
		return sdk.NewRedactedThinkingBlock(raw.Data), nil
	default:
		return sdk.ContentBlockParamUnion{}, fmt.Errorf("anthropic: unknown blob type %q", raw.Type)
	}
}

// buildTools converts tool specs to API tools, keeping extra JSON Schema keys.
func buildTools(specs []model.ToolSpec) ([]sdk.ToolUnionParam, error) {
	var out []sdk.ToolUnionParam
	for _, spec := range specs {
		schema := map[string]any{}
		if len(spec.InputSchema) > 0 {
			if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("anthropic: tool %s: input schema: %w", spec.Name, err)
			}
		}
		in := sdk.ToolInputSchemaParam{Properties: schema["properties"]}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					in.Required = append(in.Required, s)
				}
			}
		}
		for k, v := range schema {
			if k == "type" || k == "properties" || k == "required" {
				continue
			}
			if in.ExtraFields == nil {
				in.ExtraFields = map[string]any{}
			}
			in.ExtraFields[k] = v
		}
		tp := &sdk.ToolParam{Name: spec.Name, InputSchema: in}
		if spec.Description != "" {
			tp.Description = sdk.String(spec.Description)
		}
		out = append(out, sdk.ToolUnionParam{OfTool: tp})
	}
	return out, nil
}

// effort validates the effort level and converts it to the SDK type.
func effort(s string) (sdk.OutputConfigEffort, error) {
	switch s {
	case "":
		return "", nil
	case "low", "medium", "high", "xhigh", "max":
		return sdk.OutputConfigEffort(s), nil
	default:
		return "", fmt.Errorf("anthropic: unknown effort %q", s)
	}
}
