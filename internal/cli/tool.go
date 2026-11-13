package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	fstools "github.com/roman-ambinder/agentic-framework/internal/tools/fs"
)

func newToolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Run a built-in MCP tool server over stdio",
	}
	cmd.AddCommand(newToolFSCmd())
	addSandboxToolCmds(cmd)
	return cmd
}

func newToolFSCmd() *cobra.Command {
	var (
		root string
		opts fstools.Options
	)
	cmd := &cobra.Command{
		Use:   "fs",
		Short: "Serve file tools (read_file, list_dir, write_file) under a root directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			abs, err := filepath.Abs(root)
			if err != nil {
				return fmt.Errorf("fs: open root %s: %w", root, err)
			}
			r, err := os.OpenRoot(abs)
			if err != nil {
				return fmt.Errorf("fs: open root %s: %w", root, err)
			}
			defer r.Close()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err = fstools.NewServer(r, opts).Run(ctx, &mcp.StdioTransport{})
			if err != nil && (ctx.Err() != nil || errors.Is(err, io.EOF)) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "directory the tools may access (required)")
	cmd.Flags().Int64Var(&opts.MaxReadBytes, "max-read-bytes", 0, "max bytes returned by read_file (0 = default)")
	cmd.Flags().Int64Var(&opts.MaxWriteBytes, "max-write-bytes", 0, "max bytes accepted by write_file (0 = default)")
	cmd.Flags().IntVar(&opts.MaxEntries, "max-entries", 0, "max entries returned by list_dir (0 = default)")
	_ = cmd.MarkFlagRequired("root")
	return cmd
}
