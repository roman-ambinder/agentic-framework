# Shared interfaces (the contract between tasks)

This file fixes the Go package names, types and function signatures that more
than one task uses. Briefs refer to sections here as `interfaces.md §N`.
**Use these names exactly.** If a task needs to change something here, the
change goes into this file in the same PR, and the PR description says so.

Module path: `github.com/roman-ambinder/agentic-framework` (below: `<mod>`).
Generated protobuf package: `adlcv1 "<mod>/gen/go/adlc/v1"`.

## §1 Package map

| Package | Task | Purpose |
|---------|------|---------|
| `internal/eventlog` | 0.3 (done) | append-only log, `RunState`, `Fold` |
| `internal/model` | 0.4 | provider-neutral model types, `Adapter`, scripted `Fake`, errors, prices |
| `internal/model/anthropic` | 0.5b, 1.1 | Anthropic adapter |
| `internal/model/openai` | 1.2 | OpenAI adapter |
| `internal/model/router` | 1.3 | picks an adapter per request, falls back on failure |
| `internal/policy` | 0.6 | `Client` interface, static rule engine |
| `internal/toolrouter` | 0.7.1 | `Router` interface, static router, output cap |
| `internal/toolrouter/mcprouter` | 0.7.2 | `Router` backed by MCP servers |
| `internal/tools/fs` | 0.8 | fs MCP server (library); run via `adlc tool fs` |
| `internal/harness` | 0.5.x | the agent loop |
| `internal/agentconfig` | 0.9.1 | `agent.yaml` schema, loader, validation |
| `internal/observability` | 0.10 | event log → spans, text trace |
| `internal/plugin` | 0.11.1 | out-of-process plugin launcher, gRPC hook client |
| `internal/sandbox` | 1.4.x | `Sandbox` interface |
| `internal/sandbox/docker` | 1.4.x | Docker backend |
| `internal/tools/shell`, `internal/tools/git`, `internal/tools/search` | 1.5 | MCP servers that run commands in a sandbox |
| `internal/contextmgr` | 1.6.x | builds the model request from the transcript within a token budget |
| `internal/evals` | E.3, 1.8, 1.9 | fixture loader, eval runner, graders, report |
| `internal/cli` | 0.9.2 and others | cobra commands |

## §2 Model types — `internal/model`

```go
package model

// Role of a message in a conversation. The system prompt is not a message;
// it is Request.System.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// PartKind says which field of a Part is set.
type PartKind string

const (
	PartText       PartKind = "text"
	PartToolCall   PartKind = "tool_call"   // only in assistant messages
	PartToolResult PartKind = "tool_result" // only in user messages
	PartBlob       PartKind = "blob"        // only in assistant messages
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
	Name        string          // namespaced, must match ToolNamePattern
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
	Capabilities() Caps
}
```

Helper functions in `internal/model` (task 0.4):

```go
func TextPart(s string) Part
func ToolCallPart(c ToolCall) Part
func ToolResultPart(r ToolResult) Part
func BlobPart(b Blob) Part
func (m Message) Text() string        // all PartText joined with "\n"
func (m Message) ToolCalls() []ToolCall
```

### §2.1 Errors

```go
// Error is returned by adapters for failed calls.
type Error struct {
	Class      adlcv1.ErrorClass // MODEL, RATE_LIMIT, TIMEOUT, ...
	HTTPStatus int               // 0 if none
	Retryable  bool
	Err        error
}

func (e *Error) Error() string
func (e *Error) Unwrap() error

// IsRetryable reports whether err (or an *Error it wraps) is retryable.
// context.Canceled and context.DeadlineExceeded from the caller's ctx are not.
func IsRetryable(err error) bool
```

Classification every adapter uses: HTTP 429 → `RATE_LIMIT`, retryable;
408, 500, 502, 503, 504, 529 → `MODEL`, retryable; network errors and
timeouts of the HTTP request → `TIMEOUT`, retryable; any other 4xx → `MODEL`,
not retryable.

### §2.2 Prices

```go
// Price is micro-USD per million tokens. Prices come from agent config,
// never from code, so they cannot go stale silently.
type Price struct {
	InputPerMTok      int64
	OutputPerMTok     int64
	CacheReadPerMTok  int64
	CacheWritePerMTok int64
}

// Cost returns the cost of u in micro-USD, rounded up.
func (p Price) Cost(u Usage) int64
```

### §2.3 Fake model

```go
// FakeTurn is one scripted answer. Exactly one of Response and Err is set.
type FakeTurn struct {
	Response *Response
	Err      error
}

// Fake returns its script one turn per Generate call and records requests.
type Fake struct{ /* unexported */ }

func NewFake(turns ...FakeTurn) *Fake
func (f *Fake) Requests() []*Request // copies of every request received
// Generate returns the next turn; when the script is exhausted it returns
// an error wrapping ErrScriptExhausted.
var ErrScriptExhausted = errors.New("model: fake script exhausted")

// LoadFakeScript reads a JSON script (format in brief 0.4) for `adlc run --model fake`.
func LoadFakeScript(path string) ([]FakeTurn, error)
```

## §3 Event log — `internal/eventlog` (exists)

The harness depends on this interface, which `*eventlog.Store` satisfies:

```go
// in package harness
type Log interface {
	Append(ctx context.Context, ev *adlcv1.Event) (*adlcv1.Event, error)
	Read(ctx context.Context, runID string, afterSeq uint64) ([]*adlcv1.Event, error)
}
```

`eventlog.Fold`, `eventlog.RunState`, `RunState.Apply`, `RunState.Digest` and
`eventlog.Kind` are as in `internal/eventlog`.

## §4 Policy — `internal/policy`

```go
package policy

// Request is what is being authorized.
type Request struct {
	AgentID       string
	Tool          string // namespaced tool name
	Risk          adlcv1.RiskClass
	ArgumentsJSON string
}

// Decision is the answer.
type Decision struct {
	Decision adlcv1.Decision       // ALLOW, DENY, NEEDS_APPROVAL
	RuleID   string                // which rule decided; "default" if none matched
	Reason   string
	Source   adlcv1.DecisionSource // LOCAL for the static engine
}

// Client decides whether a tool call may run.
type Client interface {
	Authorize(ctx context.Context, req Request) (Decision, error)
}

// AllowAll allows everything. Tests only.
type AllowAll struct{}

// Rule matches on risk class and (optionally) tool name. First match wins.
type Rule struct {
	ID       string
	Risk     adlcv1.RiskClass // UNSPECIFIED = any risk
	Tool     string           // "" = any tool; exact match otherwise
	Decision adlcv1.Decision
}

// Static is a Client over a fixed rule list.
type Static struct {
	Rules   []Rule
	Default adlcv1.Decision // used when no rule matches; must not be UNSPECIFIED
}
```

## §5 Tool router — `internal/toolrouter`

Tool names are namespaced `<server>__<tool>` (two underscores), e.g.
`fs__read_file`. Dots are not allowed by model providers.

```go
package toolrouter

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

// Namespaced returns server + "__" + tool.
func Namespaced(server, tool string) string

// ForModel caps output for the model: if len(out) > max, it returns the
// first max bytes (cut at a UTF-8 boundary) plus a line
// "\n[output truncated: showing N of M bytes]" and truncated=true.
func ForModel(out []byte, max int) (text string, truncated bool)

// DefaultMaxModelBytes is used when config does not set a limit.
const DefaultMaxModelBytes = 16 * 1024

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
func NewStatic(tools ...StaticTool) (*Static, error)
```

`Call` returns a non-nil `error` only when the call could not be made at all
(unknown tool, server crashed, ctx cancelled). A tool that ran and failed
returns `Result{IsError: true}` and a nil error.

## §6 Harness — `internal/harness`

```go
package harness

// Config is everything about one run that is not a dependency.
type Config struct {
	RunID        string
	AgentID      string // e.g. "hello@1"
	AgentVersion string // same as AgentID for now
	Task         string // first user message
	System       string
	Model        string // passed to Request.Model
	MaxTokens    int    // per model call; 0 = 4096
	Effort       string
	Budget       *adlcv1.Budget
	Price        model.Price
	MaxToolOutputBytes int // 0 = toolrouter.DefaultMaxModelBytes
	Retry        RetryPolicy // task 0.5.3
	Hooks        []Hook      // task 0.5.4
}

// Deps are the run's collaborators.
type Deps struct {
	Log    Log
	Model  model.Adapter
	Tools  toolrouter.Router
	Policy policy.Client
	Now    func() time.Time                                  // default time.Now
	Sleep  func(ctx context.Context, d time.Duration) error // default: timer + ctx
}

// Run executes a new run until it stops and returns its final state.
// The error is non-nil only for failures to use the event log; every other
// failure ends the run with a stop reason recorded in the log.
func Run(ctx context.Context, cfg Config, deps Deps) (*eventlog.RunState, error)

// Transcript rebuilds the conversation from a run's events (used to build
// each request, and by resume in task 1.7).
func Transcript(events []*adlcv1.Event) ([]model.Message, error)
```

### §6.1 Events the harness writes, per step

One *step* = one successful model call plus the tool calls it asked for.

```
model_call_started   {call_id: "m<step>", step, provider, model, attempt}
model_call_failed    {call_id, error_class, message, will_retry, http_status}   (0..n, task 0.5.3)
model_call_completed {call_id, finish_reason, provider_finish_reason, text,
                      tool_calls, provider_blobs, latency}  cost = usage + price
for each tool call, in order:
  tool_call_requested {call{id,name,arguments_json}, risk_class, server}
  policy_decided      {call_id, decision, rule_id, reason, source}
  tool_call_completed {call_id, is_error, output, truncated_for_model, duration, exit_code}
checkpoint           {step, state_digest}                                         (task 0.5.4)
```

The run starts with `run_started` and ends with exactly one `run_completed`.

### §6.2 Stop reasons

| Situation | `run_completed.stop_reason` | `detail` |
|-----------|-----------------------------|----------|
| Model finished with `END_TURN` and no tool calls | `COMPLETED` (`result` = text) | |
| A budget limit reached (task 0.5.2) | `BUDGET_EXHAUSTED` (+ `exhausted_budget`) | which limit |
| Policy said `DENY` | `DENIED` | rule id and reason |
| Policy said `NEEDS_APPROVAL` | `AWAITING_APPROVAL` (after `approval_requested`) | |
| Model returned `REFUSAL` | `ERROR` | `"model refused"` |
| Model returned `MAX_TOKENS` with no tool calls | `ERROR` | `"output limit reached"` |
| Model call failed and is not retried | `ERROR` | error message |
| `ctx` cancelled | `CANCELLED` | |

When `ctx` is cancelled, the harness writes the final events with
`context.WithoutCancel(ctx)` so they are not lost.

## §7 Agent config — `internal/agentconfig` (task 0.9.1)

File format: brief 0.9.1. Go entry points:

```go
func Load(path string) (*Agent, error)          // parse + validate
func (a *Agent) Validate() error                // all problems, joined with errors.Join
```

## §8 Sandbox — `internal/sandbox` (task 1.4.1)

```go
package sandbox

type Spec struct {
	Image       string            // e.g. "golang:1.26"
	WorkspaceDir string           // host dir copied into the container at /workspace
	CPUs        float64           // 0 = no limit
	MemoryBytes int64             // 0 = no limit
	Network     bool              // false = no network (default)
	Env         map[string]string
}

type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Duration time.Duration
	TimedOut bool
}

type Sandbox interface {
	Create(ctx context.Context, spec Spec) (Handle, error)
	Exec(ctx context.Context, h Handle, cmd []string, timeout time.Duration) (ExecResult, error)
	CopyOut(ctx context.Context, h Handle, path string) ([]byte, error) // tar of path
	Destroy(ctx context.Context, h Handle) error
}

type Handle struct{ ID string }
```

## §9 Evals — `internal/evals` (tasks E.3, 1.8, 1.9)

Fixture layout and Go types: brief E.3. Report JSON: brief 1.9.
