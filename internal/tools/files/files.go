// Package files is an MCP server that reads, writes and lists files in a
// sandbox's workspace.
package files

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/buildinfo"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
)

// MaxWriteBytes caps write_file content.
const MaxWriteBytes = 1 << 20 // 1 MiB

// chunkChars is the base64 chunk size; a multiple of 4.
const chunkChars = 65536

// ReadInput is the input of the read_file tool.
type ReadInput struct {
	Path string `json:"path" jsonschema:"file path relative to /workspace"`
}

// WriteInput is the input of the write_file tool.
type WriteInput struct {
	Path    string `json:"path" jsonschema:"file path relative to /workspace"`
	Content string `json:"content" jsonschema:"the complete new file content"`
}

// ListInput is the input of the list_dir tool.
type ListInput struct {
	Path string `json:"path,omitempty" jsonschema:"directory relative to /workspace, default ."`
}

// NewServer returns the files MCP server for the sandbox in o.
func NewServer(o sandboxtool.Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "adlc-files", Version: buildinfo.Get().Version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a text file from the workspace.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ReadInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
		if err := sandboxtool.CheckPath(in.Path); err != nil {
			return nil, sandboxtool.ExecMeta{}, err
		}
		return stdoutOrError(ctx, o, []string{"cat", "--", in.Path})
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_dir",
		Description: "List the entries of a directory in the workspace. Directories end with /.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
		if in.Path == "" {
			in.Path = "."
		}
		if err := sandboxtool.CheckPath(in.Path); err != nil {
			return nil, sandboxtool.ExecMeta{}, err
		}
		return stdoutOrError(ctx, o, []string{"ls", "-1Ap", "--", in.Path})
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "write_file",
		Description: "Create or replace a file in the workspace with the given content. Parent directories are created.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in WriteInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
		return write(ctx, o, in)
	})
	return server
}

func stdoutOrError(ctx context.Context, o sandboxtool.Options, cmd []string) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
	res, err := o.Run(ctx, cmd, 0)
	if err != nil {
		return nil, sandboxtool.ExecMeta{}, err
	}
	if sandboxtool.Failed(res) {
		return sandboxtool.ExecError(res)
	}
	return sandboxtool.TextResult(string(res.Stdout), false), sandboxtool.Meta(res), nil
}

func write(ctx context.Context, o sandboxtool.Options, in WriteInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
	if err := sandboxtool.CheckPath(in.Path); err != nil {
		return nil, sandboxtool.ExecMeta{}, err
	}
	if in.Path == "." {
		return nil, sandboxtool.ExecMeta{}, errors.New("path must name a file")
	}
	if len(in.Content) > MaxWriteBytes {
		return nil, sandboxtool.ExecMeta{}, fmt.Errorf("content is larger than %d bytes", MaxWriteBytes)
	}
	create := []string{"sh", "-c", `mkdir -p -- "$(dirname -- "$1")" && : > "$1"`, "sh", in.Path}
	last, err := o.Run(ctx, create, 0)
	if err != nil || sandboxtool.Failed(last) {
		return failOrErr(last, err)
	}
	enc := base64.StdEncoding.EncodeToString([]byte(in.Content))
	for len(enc) > 0 {
		n := min(chunkChars, len(enc))
		appendCmd := []string{"sh", "-c", `printf '%s' "$1" | base64 -d >> "$2"`, "sh", enc[:n], in.Path}
		enc = enc[n:]
		if last, err = o.Run(ctx, appendCmd, 0); err != nil || sandboxtool.Failed(last) {
			return failOrErr(last, err)
		}
	}
	return sandboxtool.TextResult(fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), false), sandboxtool.Meta(last), nil
}

func failOrErr(res sandbox.ExecResult, err error) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
	if err != nil {
		return nil, sandboxtool.ExecMeta{}, err
	}
	return sandboxtool.ExecError(res)
}
