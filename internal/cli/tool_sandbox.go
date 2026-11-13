package cli

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/docker"
	"github.com/roman-ambinder/agentic-framework/internal/tools/files"
	"github.com/roman-ambinder/agentic-framework/internal/tools/git"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
	"github.com/roman-ambinder/agentic-framework/internal/tools/search"
	"github.com/roman-ambinder/agentic-framework/internal/tools/shell"
)

// addSandboxToolCmds adds the sandbox-backed tool servers (task 1.5) to the
// "adlc tool" command that task 0.8 defines.
func addSandboxToolCmds(cmd *cobra.Command) {
	servers := []struct {
		name  string
		short string
		build func(sandboxtool.Options) *mcp.Server
	}{
		{"shell", "Serve the shell tool for a sandbox", shell.NewServer},
		{"git", "Serve the git tools for a sandbox", git.NewServer},
		{"search", "Serve the search tool for a sandbox", search.NewServer},
		{"files", "Serve the file tools for a sandbox", files.NewServer},
	}
	for _, s := range servers {
		cmd.AddCommand(newSandboxToolCmd(s.name, s.short, s.build))
	}
}

// newSandboxToolCmd builds `adlc tool <name> --sandbox ID`. Nothing but MCP
// traffic may be written to stdout.
func newSandboxToolCmd(name, short string, build func(sandboxtool.Options) *mcp.Server) *cobra.Command {
	var sandboxID, backend string
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSandboxTool(cmd.Context(), backend, sandboxID, build)
		},
	}
	cmd.Flags().StringVar(&sandboxID, "sandbox", "", "id of the sandbox to work in (required)")
	cmd.Flags().StringVar(&backend, "backend", "docker", "sandbox backend (only docker is supported)")
	_ = cmd.MarkFlagRequired("sandbox")
	return cmd
}

func runSandboxTool(ctx context.Context, backend, id string, build func(sandboxtool.Options) *mcp.Server) error {
	if backend != "docker" {
		return fmt.Errorf("cli: unsupported sandbox backend %q (only docker)", backend)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, err := docker.New(ctx)
	if err != nil {
		return fmt.Errorf("cli: connect to sandbox backend: %w", err)
	}
	defer b.Close()
	server := build(sandboxtool.Options{Sandbox: b, Handle: sandbox.Handle{ID: id}})
	return server.Run(ctx, &mcp.StdioTransport{})
}
