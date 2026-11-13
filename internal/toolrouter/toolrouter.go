// Package toolrouter lists the tools an agent may call and runs those calls.
package toolrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// Tool is a tool the agent may call.
type Tool struct {
	Spec   model.ToolSpec   // Spec.Name is namespaced
	Server string           // server that provides it, e.g. "fs"
	Risk   adlcv1.RiskClass // never UNSPECIFIED
}

// Result is a finished tool call.
type Result struct {
	Output   []byte // full output, stored in the event log
	IsError  bool
	ExitCode int32
	Duration time.Duration
}

// Router lists tools and runs calls.
type Router interface {
	Tools() []Tool
	Call(ctx context.Context, call model.ToolCall) (Result, error)
	Close() error
}

// ErrUnknownTool is returned by Call for a name not in Tools().
var ErrUnknownTool = errors.New("toolrouter: unknown tool")

var toolNameRE = regexp.MustCompile(model.ToolNamePattern)

// Namespaced returns server + "__" + tool. It does not validate.
func Namespaced(server, tool string) string { return server + "__" + tool }

// ValidateTool checks one Tool. An empty InputSchema is allowed and means
// {"type":"object"}.
func ValidateTool(t Tool) error {
	if !toolNameRE.MatchString(t.Spec.Name) {
		return fmt.Errorf("toolrouter: tool name %q does not match %s", t.Spec.Name, model.ToolNamePattern)
	}
	if t.Risk == adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED {
		return fmt.Errorf("toolrouter: tool %q has no risk class", t.Spec.Name)
	}
	if t.Server == "" {
		return fmt.Errorf("toolrouter: tool %q has no server", t.Spec.Name)
	}
	if len(t.Spec.InputSchema) > 0 && !isJSONObject(t.Spec.InputSchema) {
		return fmt.Errorf("toolrouter: tool %q input schema is not a JSON object", t.Spec.Name)
	}
	return nil
}

// isJSONObject reports whether raw is a valid JSON object (not null, not an array).
func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}
