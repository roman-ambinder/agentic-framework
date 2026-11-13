package contextmgr

import "github.com/roman-ambinder/agentic-framework/internal/model"

// EstimateTokens returns (len(s)+3)/4, the estimated token count of s.
func EstimateTokens(s string) int64 {
	return estimateBytes(len(s))
}

func estimateBytes(n int) int64 {
	return (int64(n) + 3) / 4
}

// EstimateRequest estimates the whole request: System, every tool's
// Name + Description + InputSchema, and every part of every message
// (text; tool call name + arguments; tool result content; blob data).
// Each string is rounded up separately, so the estimate of a request is the
// sum of the estimates of its pieces.
func EstimateRequest(req *model.Request) int64 {
	total := EstimateTokens(req.System)
	for _, t := range req.Tools {
		total += EstimateTokens(t.Name) + EstimateTokens(t.Description) + estimateBytes(len(t.InputSchema))
	}
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			total += estimatePart(p)
		}
	}
	return total
}

func estimatePart(p model.Part) int64 {
	total := EstimateTokens(p.Text)
	if p.ToolCall != nil {
		total += EstimateTokens(p.ToolCall.Name) + estimateBytes(len(p.ToolCall.Arguments))
	}
	if p.ToolResult != nil {
		total += EstimateTokens(p.ToolResult.Content)
	}
	if p.Blob != nil {
		total += estimateBytes(len(p.Blob.Data))
	}
	return total
}
