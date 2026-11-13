package plugin

import (
	"google.golang.org/protobuf/types/known/durationpb"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// toProtoCall converts a model tool call to its protobuf form.
func toProtoCall(call model.ToolCall) *adlcv1.ToolCall {
	return &adlcv1.ToolCall{Id: call.ID, Name: call.Name, ArgumentsJson: string(call.Arguments)}
}

// toProtoInfo describes a call and the tool it maps to.
func toProtoInfo(call model.ToolCall, tool toolrouter.Tool) *adlcv1.ToolCallInfo {
	return &adlcv1.ToolCallInfo{Call: toProtoCall(call), RiskClass: tool.Risk, Server: tool.Server}
}

// toPostToolRequest builds a PostTool request. There is no toolrouter.Tool
// at that point, so info carries only the call; the rest comes from res.
func toPostToolRequest(call model.ToolCall, res toolrouter.Result) *adlcv1.PostToolRequest {
	return &adlcv1.PostToolRequest{
		Info:     &adlcv1.ToolCallInfo{Call: toProtoCall(call)},
		IsError:  res.IsError,
		Output:   res.Output,
		ExitCode: res.ExitCode,
		Duration: durationpb.New(res.Duration),
	}
}
