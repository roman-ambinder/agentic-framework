// Package git is an MCP server with read-only git tools for a sandbox's
// workspace.
package git

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/buildinfo"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
)

// StatusInput is the (empty) input of the status tool.
type StatusInput struct{}

// DiffInput is the input of the diff tool.
type DiffInput struct {
	Path   string `json:"path,omitempty" jsonschema:"limit the diff to this file or directory"`
	Staged bool   `json:"staged,omitempty" jsonschema:"show staged changes instead of unstaged ones"`
}

// NewServer returns the git MCP server for the sandbox in o.
func NewServer(o sandboxtool.Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "adlc-git", Version: buildinfo.Get().Version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "status",
		Description: "Show changed, staged and untracked files in the workspace (git status).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ StatusInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
		return exec(ctx, o, []string{"git", "status", "--porcelain=v1", "--branch", "--untracked-files=all"})
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "diff",
		Description: "Show line changes in tracked files (git diff). New untracked files are listed by git__status, not here.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DiffInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
		if in.Path != "" {
			if err := sandboxtool.CheckPath(in.Path); err != nil {
				return nil, sandboxtool.ExecMeta{}, err
			}
		}
		return exec(ctx, o, diffArgs(in))
	})
	return server
}

func diffArgs(in DiffInput) []string {
	cmd := []string{"git", "--no-pager", "diff", "--no-color", "--no-ext-diff"}
	if in.Staged {
		cmd = append(cmd, "--staged")
	}
	if in.Path != "" {
		cmd = append(cmd, "--", in.Path)
	}
	return cmd
}

func exec(ctx context.Context, o sandboxtool.Options, cmd []string) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
	res, err := o.Run(ctx, cmd, 0)
	if err != nil {
		return nil, sandboxtool.ExecMeta{}, err
	}
	return sandboxtool.TextResult(sandboxtool.Format(res), res.ExitCode != 0), sandboxtool.Meta(res), nil
}
