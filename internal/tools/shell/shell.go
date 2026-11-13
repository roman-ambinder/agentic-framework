// Package shell is an MCP server whose only tool runs a shell command in a
// sandbox's workspace.
package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/buildinfo"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
)

// RunInput is the input of the run tool.
type RunInput struct {
	Command        string `json:"command" jsonschema:"shell command; runs with sh -c in /workspace"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"timeout in seconds, default 120, max 600"`
}

// NewServer returns the shell MCP server for the sandbox in o.
func NewServer(o sandboxtool.Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "adlc-shell", Version: buildinfo.Get().Version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "run",
		Description: "Run a shell command in the project workspace (/workspace) and return its exit code, stdout and stderr.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RunInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
		return run(ctx, o, in)
	})
	return server
}

func run(ctx context.Context, o sandboxtool.Options, in RunInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, sandboxtool.ExecMeta{}, errors.New("command is empty")
	}
	maxSecs := int(o.EffectiveMaxTimeout().Seconds())
	if in.TimeoutSeconds < 0 || in.TimeoutSeconds > maxSecs {
		return nil, sandboxtool.ExecMeta{}, fmt.Errorf("timeout_seconds must be between 1 and %d", maxSecs)
	}
	res, err := o.Run(ctx, []string{"sh", "-c", in.Command}, time.Duration(in.TimeoutSeconds)*time.Second)
	if err != nil {
		return nil, sandboxtool.ExecMeta{}, err
	}
	return sandboxtool.TextResult(sandboxtool.Format(res), sandboxtool.Failed(res)), sandboxtool.Meta(res), nil
}
