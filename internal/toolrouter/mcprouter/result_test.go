package mcprouter

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToResult(t *testing.T) {
	tests := []struct {
		name  string
		res   *mcp.CallToolResult
		want  string
		isErr bool
	}{
		{"text joined", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "a"}, &mcp.TextContent{Text: "b"}}}, "a\nb", false},
		{"non text placeholder", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "a"}, &mcp.AudioContent{}}}, "a\n[audiocontent content omitted]", false},
		{"structured fallback", &mcp.CallToolResult{StructuredContent: map[string]any{"k": 1}}, `{"k":1}`, false},
		{"text wins over structured", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "t"}}, StructuredContent: 1}, "t", false},
		{"is error", &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "bad"}}}, "bad", true},
		{"empty", &mcp.CallToolResult{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toResult(tt.res)
			if string(got.Output) != tt.want || got.IsError != tt.isErr || got.ExitCode != 0 {
				t.Fatalf("toResult = %q, %v, %d; want %q, %v, 0", got.Output, got.IsError, got.ExitCode, tt.want, tt.isErr)
			}
		})
	}
}
