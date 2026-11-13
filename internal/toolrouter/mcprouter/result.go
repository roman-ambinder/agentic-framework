package mcprouter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// toResult converts an MCP tool result to a toolrouter.Result. Text content is
// kept; other content kinds are replaced by a short placeholder.
func toResult(res *mcp.CallToolResult) toolrouter.Result {
	pieces := make([]string, 0, len(res.Content))
	for _, item := range res.Content {
		if text, ok := item.(*mcp.TextContent); ok {
			pieces = append(pieces, text.Text)
			continue
		}
		kind := strings.ToLower(strings.TrimPrefix(fmt.Sprintf("%T", item), "*mcp."))
		pieces = append(pieces, fmt.Sprintf("[%s content omitted]", kind))
	}
	out := strings.Join(pieces, "\n")
	if out == "" && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			out = string(b)
		}
	}
	return toolrouter.Result{Output: []byte(out), IsError: res.IsError}
}
