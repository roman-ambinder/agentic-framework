// Package sandboxset serves the four sandbox tool servers (shell, git,
// search, files) in process and connects them to an MCP router.
package sandboxset

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter/mcprouter"
	"github.com/roman-ambinder/agentic-framework/internal/tools/files"
	"github.com/roman-ambinder/agentic-framework/internal/tools/git"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
	"github.com/roman-ambinder/agentic-framework/internal/tools/search"
	"github.com/roman-ambinder/agentic-framework/internal/tools/shell"
)

// ServerConfigs returns the four servers' configs with their risk classes.
// Command is the subprocess form ["adlc", "tool", <name>, "--sandbox",
// sandboxID]; NewRouter does not run it.
func ServerConfigs(sandboxID string) []mcprouter.ServerConfig {
	cmd := func(name string) []string {
		return []string{"adlc", "tool", name, "--sandbox", sandboxID}
	}
	return []mcprouter.ServerConfig{
		{Name: "shell", Command: cmd("shell"), Risk: map[string]adlcv1.RiskClass{"run": adlcv1.RiskClass_RISK_CLASS_EXEC}},
		{Name: "git", Command: cmd("git"), DefaultRisk: adlcv1.RiskClass_RISK_CLASS_READ},
		{Name: "search", Command: cmd("search"), DefaultRisk: adlcv1.RiskClass_RISK_CLASS_READ},
		{
			Name: "files", Command: cmd("files"), DefaultRisk: adlcv1.RiskClass_RISK_CLASS_READ,
			Risk: map[string]adlcv1.RiskClass{"write_file": adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL},
		},
	}
}

// NewRouter serves shell, git, search and files for o.Sandbox/o.Handle in
// this process and returns a router over them. Close the router when done.
func NewRouter(ctx context.Context, o sandboxtool.Options) (*mcprouter.Router, error) {
	cfgs := ServerConfigs(o.Handle.ID)
	servers := []*mcp.Server{shell.NewServer(o), git.NewServer(o), search.NewServer(o), files.NewServer(o)}
	conns := make([]mcprouter.Conn, 0, len(cfgs))
	for i, cfg := range cfgs {
		ct, st := mcp.NewInMemoryTransports()
		if _, err := servers[i].Connect(ctx, st, nil); err != nil {
			return nil, fmt.Errorf("sandboxset: serve %s: %w", cfg.Name, err)
		}
		conns = append(conns, mcprouter.Conn{Config: cfg, Transport: ct})
	}
	return mcprouter.Connect(ctx, conns)
}
