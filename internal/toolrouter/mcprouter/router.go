package mcprouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/buildinfo"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// Conn is a server plus an already-built transport (used by tests and by Start).
type Conn struct {
	Config    ServerConfig
	Transport mcp.Transport
}

// Router implements toolrouter.Router over MCP sessions.
type Router struct {
	tools    []toolrouter.Tool
	byName   map[string]entry // namespaced name -> entry
	sessions []*mcp.ClientSession
	now      func() time.Time
}

type entry struct {
	session *mcp.ClientSession
	tool    string // original (not namespaced) name
}

var _ toolrouter.Router = (*Router)(nil)

// Start validates each config, launches every server as a subprocess and
// connects to it.
func Start(ctx context.Context, servers []ServerConfig) (*Router, error) {
	conns := make([]Conn, 0, len(servers))
	for _, c := range servers {
		if err := c.Validate(); err != nil {
			return nil, err
		}
		// Not exec.CommandContext: the router outlives ctx.
		cmd := exec.Command(c.Command[0], c.Command[1:]...)
		cmd.Dir = c.Dir
		cmd.Env = os.Environ()
		keys := make([]string, 0, len(c.Env))
		for k := range c.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			cmd.Env = append(cmd.Env, k+"="+c.Env[k])
		}
		cmd.Stderr = os.Stderr
		conns = append(conns, Conn{Config: c, Transport: &mcp.CommandTransport{Command: cmd}})
	}
	return Connect(ctx, conns)
}

// Connect connects over the given transports. It does not validate the
// configs' commands and never runs them.
func Connect(ctx context.Context, conns []Conn) (*Router, error) {
	seen := make(map[string]bool, len(conns))
	for _, c := range conns {
		if seen[c.Config.Name] {
			return nil, fmt.Errorf("mcprouter: duplicate server %q", c.Config.Name)
		}
		seen[c.Config.Name] = true
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "adlc", Version: buildinfo.Get().Version}, nil)
	r := &Router{byName: map[string]entry{}, now: time.Now}
	for _, conn := range conns {
		if err := r.addServer(ctx, client, conn); err != nil {
			_ = r.Close()
			return nil, err
		}
	}
	return r, nil
}

func (r *Router) addServer(ctx context.Context, client *mcp.Client, conn Conn) error {
	name := conn.Config.Name
	session, err := client.Connect(ctx, conn.Transport, nil)
	if err != nil {
		return fmt.Errorf("mcprouter: connect %s: %w", name, err)
	}
	r.sessions = append(r.sessions, session)
	offered := map[string]bool{}
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return fmt.Errorf("mcprouter: list tools of %s: %w", name, err)
		}
		offered[t.Name] = true
		tool, err := buildTool(conn.Config, t)
		if err != nil {
			return err
		}
		if _, dup := r.byName[tool.Spec.Name]; dup {
			return fmt.Errorf("mcprouter: duplicate tool %q", tool.Spec.Name)
		}
		r.byName[tool.Spec.Name] = entry{session: session, tool: t.Name}
		r.tools = append(r.tools, tool)
	}
	for _, k := range sortedKeys(conn.Config.Risk) {
		if !offered[k] {
			return fmt.Errorf("mcprouter: server %q has no tool %q (listed in risk)", name, k)
		}
	}
	return nil
}

func buildTool(cfg ServerConfig, t *mcp.Tool) (toolrouter.Tool, error) {
	schema, err := json.Marshal(t.InputSchema)
	if err != nil {
		return toolrouter.Tool{}, fmt.Errorf("mcprouter: input schema of tool %q of server %q: %w", t.Name, cfg.Name, err)
	}
	risk := cfg.Risk[t.Name]
	if risk == adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED {
		risk = cfg.DefaultRisk
	}
	if risk == adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED {
		return toolrouter.Tool{}, fmt.Errorf("mcprouter: tool %q of server %q has no risk class (set risk or default_risk)", t.Name, cfg.Name)
	}
	tool := toolrouter.Tool{
		Spec:   model.ToolSpec{Name: toolrouter.Namespaced(cfg.Name, t.Name), Description: t.Description, InputSchema: schema},
		Server: cfg.Name,
		Risk:   risk,
	}
	if err := toolrouter.ValidateTool(tool); err != nil {
		return toolrouter.Tool{}, err
	}
	return tool, nil
}

func sortedKeys(m map[string]adlcv1.RiskClass) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Tools returns a copy of the tool list: servers in order, tools in the order
// each server listed them.
func (r *Router) Tools() []toolrouter.Tool {
	out := make([]toolrouter.Tool, len(r.tools))
	for i, t := range r.tools {
		t.Spec.InputSchema = append(json.RawMessage(nil), t.Spec.InputSchema...)
		out[i] = t
	}
	return out
}

// Call runs the named tool. A tool that fails returns Result.IsError with a nil
// error; a non-nil error means the call could not be made.
func (r *Router) Call(ctx context.Context, call model.ToolCall) (toolrouter.Result, error) {
	e, ok := r.byName[call.Name]
	if !ok {
		return toolrouter.Result{}, fmt.Errorf("%w: %q", toolrouter.ErrUnknownTool, call.Name)
	}
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	start := r.now()
	res, err := e.session.CallTool(ctx, &mcp.CallToolParams{Name: e.tool, Arguments: args})
	if err != nil {
		return toolrouter.Result{}, fmt.Errorf("mcprouter: call %s: %w", call.Name, err)
	}
	out := toResult(res)
	out.Duration = r.now().Sub(start)
	return out, nil
}

// Close closes every session in reverse order. Calling it again is a no-op.
func (r *Router) Close() error {
	var errs []error
	for i := len(r.sessions) - 1; i >= 0; i-- {
		errs = append(errs, r.sessions[i].Close())
	}
	r.sessions = nil
	return errors.Join(errs...)
}
