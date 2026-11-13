// Package search is an MCP server that searches file contents in a sandbox's
// workspace.
package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/roman-ambinder/agentic-framework/internal/buildinfo"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
)

const (
	defaultMaxResults = 200
	maxMaxResults     = 1000
)

// GrepInput is the input of the grep tool.
type GrepInput struct {
	Pattern    string `json:"pattern" jsonschema:"regular expression (extended syntax)"`
	Path       string `json:"path,omitempty" jsonschema:"file or directory to search, default ."`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"maximum matching lines to return, default 200, max 1000"`
}

type searcher struct {
	o     sandboxtool.Options
	once  sync.Once
	useRg bool
	err   error
}

// NewServer returns the search MCP server for the sandbox in o.
func NewServer(o sandboxtool.Options) *mcp.Server {
	s := &searcher{o: o}
	server := mcp.NewServer(&mcp.Implementation{Name: "adlc-search", Version: buildinfo.Get().Version}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "grep",
		Description: "Search file contents in the workspace for a regular expression. Returns path:line:text lines.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in GrepInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
		return s.grep(ctx, in)
	})
	return server
}

// program decides once whether ripgrep is installed in the sandbox.
func (s *searcher) program(ctx context.Context) (bool, error) {
	s.once.Do(func() {
		res, err := s.o.Run(ctx, []string{"sh", "-c", "command -v rg"}, 0)
		s.useRg, s.err = err == nil && res.ExitCode == 0, err
	})
	return s.useRg, s.err
}

func (s *searcher) grep(ctx context.Context, in GrepInput) (*mcp.CallToolResult, sandboxtool.ExecMeta, error) {
	in, err := normalize(in)
	if err != nil {
		return nil, sandboxtool.ExecMeta{}, err
	}
	useRg, err := s.program(ctx)
	if err != nil {
		return nil, sandboxtool.ExecMeta{}, err
	}
	res, err := s.o.Run(ctx, command(useRg, in), 0)
	if err != nil {
		return nil, sandboxtool.ExecMeta{}, err
	}
	meta := sandboxtool.Meta(res)
	switch {
	case res.ExitCode == 0 && !res.TimedOut:
		return sandboxtool.TextResult(limitLines(string(res.Stdout), in.MaxResults), false), meta, nil
	case res.ExitCode == 1 && !res.TimedOut && len(res.Stderr) == 0:
		return sandboxtool.TextResult("no matches", false), meta, nil
	}
	return sandboxtool.TextResult(sandboxtool.Format(res), true), meta, nil
}

func normalize(in GrepInput) (GrepInput, error) {
	if in.Pattern == "" {
		return in, errors.New("pattern is empty")
	}
	if in.Path == "" {
		in.Path = "."
	}
	if err := sandboxtool.CheckPath(in.Path); err != nil {
		return in, err
	}
	if in.MaxResults == 0 {
		in.MaxResults = defaultMaxResults
	}
	if in.MaxResults < 0 || in.MaxResults > maxMaxResults {
		return in, fmt.Errorf("max_results must be between 1 and %d", maxMaxResults)
	}
	return in, nil
}

func command(useRg bool, in GrepInput) []string {
	cmd := []string{"grep", "-rnIE", "--exclude-dir=.git"}
	if useRg {
		cmd = []string{"rg", "--line-number", "--no-heading", "--color", "never", "--hidden", "--glob", "!.git"}
	}
	if in.IgnoreCase {
		cmd = append(cmd, "-i")
	}
	return append(cmd, "-e", in.Pattern, "--", in.Path)
}

func limitLines(out string, max int) string {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) <= max {
		return strings.Join(lines, "\n")
	}
	more := len(lines) - max
	return strings.Join(lines[:max], "\n") + fmt.Sprintf("\n[%d more matching lines not shown]", more)
}
