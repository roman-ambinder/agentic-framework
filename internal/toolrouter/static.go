package toolrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// Func implements one tool in Go.
type Func func(ctx context.Context, args json.RawMessage) (Result, error)

// StaticTool pairs a tool with its implementation.
type StaticTool struct {
	Tool Tool
	Fn   Func
}

// Static is a Router over Go functions (tests and built-ins).
// NewStatic returns an error if two tools share a name, a name does not
// match model.ToolNamePattern, or a Risk is UNSPECIFIED.
type Static struct {
	tools []Tool          // in the order given to NewStatic
	fns   map[string]Func // key: Tool.Spec.Name
	now   func() time.Time
}

var _ Router = (*Static)(nil)

// NewStatic builds a Static router from the given tools. It fails on the first
// invalid tool, a nil Fn, or a duplicate tool name.
func NewStatic(tools ...StaticTool) (*Static, error) {
	s := &Static{fns: make(map[string]Func, len(tools)), now: time.Now}
	for _, entry := range tools {
		if err := ValidateTool(entry.Tool); err != nil {
			return nil, err
		}
		name := entry.Tool.Spec.Name
		if entry.Fn == nil {
			return nil, fmt.Errorf("toolrouter: tool %q has no function", name)
		}
		if _, dup := s.fns[name]; dup {
			return nil, fmt.Errorf("toolrouter: duplicate tool %q", name)
		}
		s.fns[name] = entry.Fn
		s.tools = append(s.tools, cloneTool(entry.Tool))
	}
	return s, nil
}

// Tools returns a copy of the tool list in NewStatic order.
func (s *Static) Tools() []Tool {
	out := make([]Tool, len(s.tools))
	for i, t := range s.tools {
		out[i] = cloneTool(t)
	}
	return out
}

// Call runs the named tool. A tool that fails returns Result.IsError with a nil
// error; a non-nil error means the call could not be made.
func (s *Static) Call(ctx context.Context, call model.ToolCall) (Result, error) {
	fn, ok := s.fns[call.Name]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownTool, call.Name)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	start := s.now()
	res, err := fn(ctx, args)
	if err != nil {
		return Result{}, fmt.Errorf("toolrouter: %s: %w", call.Name, err)
	}
	if res.Duration == 0 {
		res.Duration = s.now().Sub(start)
	}
	return res, nil
}

// Close releases nothing and returns nil.
func (s *Static) Close() error { return nil }

// cloneTool copies a Tool, including its schema bytes, so callers cannot alias
// the router's state.
func cloneTool(t Tool) Tool {
	if t.Spec.InputSchema != nil {
		t.Spec.InputSchema = append(json.RawMessage(nil), t.Spec.InputSchema...)
	}
	return t
}
