// Package toolstest connects to an MCP server in memory so tests can call its
// tools.
package toolstest

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Connect starts server in memory and returns a connected client session.
// It closes both ends in t.Cleanup.
func Connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("toolstest: serve: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("toolstest: connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// Call calls tool name with args and returns the result; it fails the test
// on a protocol error.
func Call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("toolstest: call %s: %v", name, err)
	}
	return res
}

// Text joins the TextContent items of r.
func Text(r *mcp.CallToolResult) string {
	var parts []string
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}
