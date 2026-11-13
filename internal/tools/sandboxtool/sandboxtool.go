// Package sandboxtool holds the helpers shared by the MCP servers that act on
// a sandbox's workspace (shell, git, search, files).
package sandboxtool

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

const (
	defaultTimeout = 2 * time.Minute
	maxTimeout     = 10 * time.Minute
)

// Options says which sandbox a tool server works on.
type Options struct {
	Sandbox        sandbox.Sandbox
	Handle         sandbox.Handle
	DefaultTimeout time.Duration // 0 = 2 * time.Minute
	MaxTimeout     time.Duration // 0 = 10 * time.Minute
}

// ExecMeta is the structured part of every exec-based tool result.
type ExecMeta struct {
	ExitCode int  `json:"exit_code"`
	TimedOut bool `json:"timed_out"`
}

// EffectiveDefaultTimeout returns DefaultTimeout, or 2 minutes when it is 0.
func (o Options) EffectiveDefaultTimeout() time.Duration {
	if o.DefaultTimeout > 0 {
		return o.DefaultTimeout
	}
	return defaultTimeout
}

// EffectiveMaxTimeout returns MaxTimeout, or 10 minutes when it is 0.
func (o Options) EffectiveMaxTimeout() time.Duration {
	if o.MaxTimeout > 0 {
		return o.MaxTimeout
	}
	return maxTimeout
}

// Run executes cmd in the sandbox with timeout (0 = DefaultTimeout).
// It returns an error only if the command could not be run at all.
func (o Options) Run(ctx context.Context, cmd []string, timeout time.Duration) (sandbox.ExecResult, error) {
	if timeout <= 0 {
		timeout = o.EffectiveDefaultTimeout()
	}
	res, err := o.Sandbox.Exec(ctx, o.Handle, cmd, timeout)
	if err != nil {
		return sandbox.ExecResult{}, fmt.Errorf("sandboxtool: exec %q: %w", cmd[0], err)
	}
	return res, nil
}

// Format renders a result for the model:
//
//	exit_code: 1
//	timed_out: true
//	--- stdout ---
//	...
//	--- stderr ---
//	...
//
// The timed_out line appears only when TimedOut. A section whose content is
// empty is written as "(empty)". Every line, including the last, ends in a
// newline. Format never truncates.
func Format(r sandbox.ExecResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit_code: %d\n", r.ExitCode)
	if r.TimedOut {
		b.WriteString("timed_out: true\n")
	}
	b.WriteString("--- stdout ---\n")
	writeSection(&b, r.Stdout)
	b.WriteString("--- stderr ---\n")
	writeSection(&b, r.Stderr)
	return b.String()
}

func writeSection(b *strings.Builder, content []byte) {
	if len(content) == 0 {
		b.WriteString("(empty)\n")
		return
	}
	b.Write(content)
	if !bytes.HasSuffix(content, []byte("\n")) {
		b.WriteString("\n")
	}
}

// TextResult builds an MCP result with one TextContent.
func TextResult(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: isError,
	}
}

// Meta returns the ExecMeta of r.
func Meta(r sandbox.ExecResult) ExecMeta {
	return ExecMeta{ExitCode: r.ExitCode, TimedOut: r.TimedOut}
}

// Failed reports whether r is a failed command (non-zero exit or timeout).
func Failed(r sandbox.ExecResult) bool { return r.ExitCode != 0 || r.TimedOut }

// ExecError wraps a non-zero or timed-out result as a tool error result.
func ExecError(r sandbox.ExecResult) (*mcp.CallToolResult, ExecMeta, error) {
	return TextResult(Format(r), true), Meta(r), nil
}

// CheckPath returns an error unless p is "." or a relative path that stays
// inside the workspace (filepath.IsLocal).
func CheckPath(p string) error {
	if p == "." || filepath.IsLocal(p) {
		return nil
	}
	return fmt.Errorf("path %q must be relative to the workspace and must not contain \"..\"", p)
}
