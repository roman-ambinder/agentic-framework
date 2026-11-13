// Package model defines the provider-neutral types for calling a language
// model, the Adapter interface every provider implements, a scripted Fake
// adapter for tests, and error and price helpers. It is a leaf package: it
// imports only the generated protobuf package.
package model

import (
	"context"
	"encoding/json"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// Role of a message in a conversation. The system prompt is not a message;
// it is Request.System.
type Role string

const (
	// RoleUser marks a message from the user (including tool results).
	RoleUser Role = "user"
	// RoleAssistant marks a message produced by the model.
	RoleAssistant Role = "assistant"
)

// PartKind says which field of a Part is set.
type PartKind string

const (
	// PartText is plain text.
	PartText PartKind = "text"
	// PartToolCall is a tool call; only in assistant messages.
	PartToolCall PartKind = "tool_call"
	// PartToolResult is a tool result; only in user messages.
	PartToolResult PartKind = "tool_result"
	// PartBlob is provider-specific content; only in assistant messages.
	PartBlob PartKind = "blob"
)

// Part is one piece of a message. Exactly the field named by Kind is set.
type Part struct {
	Kind       PartKind
	Text       string      // PartText
	ToolCall   *ToolCall   // PartToolCall
	ToolResult *ToolResult // PartToolResult
	Blob       *Blob       // PartBlob
}

// Message is one turn of the conversation.
type Message struct {
	Role  Role
	Parts []Part
}

// ToolCall is a call the model asked for. Name is the namespaced tool name
// (§5). Arguments is a JSON object.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ToolResult answers the ToolCall with the same ID.
type ToolResult struct {
	CallID  string
	Content string // what the model sees (already capped, §5)
	IsError bool
}

// Blob is provider-specific content (for example a thinking block) that must
// be sent back unchanged on the next request to the same provider. Adapters
// for other providers drop blobs they do not own.
type Blob struct {
	Provider string // adapter's Provider() value
	Kind     string // provider-defined, e.g. "thinking"
	Data     []byte // opaque
}

// ToolSpec describes a tool to the model.
type ToolSpec struct {
	Name        string // namespaced, must match ToolNamePattern
	Description string
	InputSchema json.RawMessage // JSON Schema object, "type": "object"
}

// ToolNamePattern is what every provider accepts for a tool name.
const ToolNamePattern = `^[a-zA-Z0-9_-]{1,64}$`

// Request is one model call.
type Request struct {
	Model     string // provider's model id, from agent config
	System    string
	Messages  []Message
	Tools     []ToolSpec
	MaxTokens int    // max output tokens for this call
	Effort    string // "", "low", "medium", "high", "xhigh", "max"; "" = adapter default
}

// Response is the result of one model call. Message.Role is RoleAssistant.
type Response struct {
	Message        Message
	Finish         adlcv1.FinishReason
	ProviderFinish string // provider's raw stop reason, for debugging
	Usage          Usage
	// Provider and Model that produced this response (task 1.3). Adapters
	// set Provider to their Provider() value and Model to the request's
	// model. A router sets them to the candidate that answered.
	Provider string
	Model    string
}

// Usage is the token usage of one call.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// Caps describes what an adapter supports.
type Caps struct {
	ToolCalling      bool
	PromptCaching    bool
	Streaming        bool
	MaxContextTokens int // 0 = unknown
}

// Adapter is a model provider.
type Adapter interface {
	// Provider returns a short stable name: "fake", "anthropic", "openai".
	Provider() string
	// Generate makes one model call. It never forces a tool call: providers
	// get tool_choice "auto" (some models reject forced tool choice).
	Generate(ctx context.Context, req *Request) (*Response, error)
	// CountTokens estimates the input tokens of req.
	CountTokens(ctx context.Context, req *Request) (int64, error)
	// Capabilities reports what the adapter supports.
	Capabilities() Caps
}

// TextPart returns a text part.
func TextPart(s string) Part { return Part{Kind: PartText, Text: s} }

// ToolCallPart returns a tool-call part holding a copy of c.
func ToolCallPart(c ToolCall) Part { return Part{Kind: PartToolCall, ToolCall: &c} }

// ToolResultPart returns a tool-result part holding a copy of r.
func ToolResultPart(r ToolResult) Part { return Part{Kind: PartToolResult, ToolResult: &r} }

// BlobPart returns a blob part holding a copy of b.
func BlobPart(b Blob) Part { return Part{Kind: PartBlob, Blob: &b} }

// Text joins the Text of every PartText part with "\n". No parts gives "".
func (m Message) Text() string {
	var out string
	first := true
	for _, p := range m.Parts {
		if p.Kind != PartText {
			continue
		}
		if !first {
			out += "\n"
		}
		out += p.Text
		first = false
	}
	return out
}

// ToolCalls returns the ToolCall of every PartToolCall part, in order. It
// returns nil when there are none.
func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, p := range m.Parts {
		if p.Kind == PartToolCall && p.ToolCall != nil {
			calls = append(calls, *p.ToolCall)
		}
	}
	return calls
}
