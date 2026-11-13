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
| `internal/model/modeltest` | 0.4 | shared adapter contract scenarios (§2.4) |
| `internal/model/anthropic` | 0.5b, 1.1 | Anthropic adapter (§2.5) |
| `internal/model/openai` | 1.2 | OpenAI adapter |
| `internal/model/router` | 1.3 | picks an adapter per request, falls back on failure |
| `internal/policy` | 0.6 | `Client` interface, static rule engine |
| `internal/toolrouter` | 0.7.1 | `Router` interface, static router, output cap |
| `internal/toolrouter/mcprouter` | 0.7.2 | `Router` backed by MCP servers |
| `internal/tools/fs` | 0.8 | fs MCP server on the host (library); run via `adlc tool fs` |
| `internal/harness` | 0.5.x | the agent loop |
| `internal/agentconfig` | 0.9.1 | `agent.yaml` schema, loader, validation |
| `internal/observability` | 0.10 | event log → spans, text trace |
| `internal/plugin` | 0.11.1 | out-of-process plugin launcher; `*plugin.Process` implements `harness.Hook` |
| `contracts/proto/adlc/v1/plugin.proto` | 0.11.1 | gRPC service `HookService` (`PreTool`, `PostTool`) |
| `internal/sandbox` | 1.4.x | `Sandbox` interface |
| `internal/sandbox/docker` | 1.4.x | Docker backend |
| `internal/tools/shell`, `internal/tools/git`, `internal/tools/search`, `internal/tools/files` | 1.5 | MCP servers that act on a sandbox's `/workspace` |
| `internal/tools/sandboxtool` | 1.5 | shared helpers for those servers (`Options`, `Run`, `Format`) |
| `internal/tools/sandboxset` | 1.5 | in-process router over the four sandbox tool servers (§8) |
| `internal/contextmgr` | 1.6.x | builds the model request from the transcript within a token budget |
| `internal/evals` | E.3, 1.8, 1.9 | suite loader, workspace preparation, eval runner, graders, report (§9) |
| `internal/costreport` | 1.11 | cost report from cost events (`adlc cost`) |
| `evals/fixtures/ledger` | E.1 | the fixture Go project (own module `example.com/ledger`) |
| `evals/suites/go-bugs/<bug-id>/` | E.2, format E.3 | seeded bugs (§9); `evals/suites/go.mod` keeps them out of the main module |
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

// ClassifyHTTP maps a provider HTTP status to an error class (task 0.4).
func ClassifyHTTP(status int) (class adlcv1.ErrorClass, retryable bool)
```

| status | class | retryable |
|--------|-------|-----------|
| 429 | `RATE_LIMIT` | true |
| 408, 500, 502, 503, 504, 529 | `MODEL` | true |
| any other 400–499 | `MODEL` | false |
| any other 500–599 | `MODEL` | true |
| anything else (incl. 0) | `INTERNAL` | false |

Network errors and timeouts of the HTTP request → `TIMEOUT`, retryable.
Adapters disable the SDK's own retries (`option.WithMaxRetries(0)`): the
harness retries (task 0.5.3).

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
func (f *Fake) Remaining() int       // turns not yet used
// Generate returns the next turn; when the script is exhausted it returns
// an error wrapping ErrScriptExhausted.
var ErrScriptExhausted = errors.New("model: fake script exhausted")

// ParseFakeScript decodes a fake script (format: brief 0.4, "Fake script JSON").
func ParseFakeScript(data []byte) ([]FakeTurn, error)
// LoadFakeScript reads the file and calls ParseFakeScript (used by `adlc run --fake-script`).
func LoadFakeScript(path string) ([]FakeTurn, error)
```

Fake script format: `{"turns": [ {"text", "tool_calls": [{"id","name","arguments"}],
"finish", "usage": {"input_tokens","output_tokens","cache_read_tokens","cache_write_tokens"}}
| {"error": {"class","http_status","retryable","message"}} ]}` — no wrapper
object around a turn; full rules in brief 0.4.

### §2.4 Adapter contract tests — `internal/model/modeltest` (task 0.4)

```go
package modeltest

// Scenario is one model call and the provider-neutral result an adapter must
// produce for it. Exactly one of Want and WantErr is set.
type Scenario struct {
	Name    string
	Request *model.Request
	Want    *model.Response
	WantErr *model.Error // only Class, HTTPStatus and Retryable are compared
}

const (
	TextReply           = "text_reply"
	ToolCall            = "tool_call"
	ParallelToolCalls   = "parallel_tool_calls"
	ToolResultRoundTrip = "tool_result_round_trip"
	Refusal             = "refusal"
	MaxTokens           = "max_tokens"
	RateLimited         = "rate_limited"
	ServerError         = "server_error"
	BadRequest          = "bad_request"
)

func Standard() []Scenario
func Run(t *testing.T, newAdapter func(t *testing.T, sc Scenario) model.Adapter)
// CheckResponse ignores ProviderFinish, Provider, Model and Blob.Data, and
// compares ToolCall.Arguments as JSON values.
func CheckResponse(t testing.TB, got, want *model.Response)
func CheckError(t testing.TB, err error, want *model.Error)
```

Every adapter (0.5b, 1.2) has a `TestContract` that calls `modeltest.Run`
with an httptest server answering each scenario.

### §2.5 Adapter packages

```go
package anthropic // task 0.5b; 1.1 adds Options.DisableCaching
const ProviderName = "anthropic"
type Options struct {
	APIKey           string       // "" = ANTHROPIC_API_KEY
	BaseURL          string       // "" = SDK default
	HTTPClient       *http.Client // nil = SDK default
	MaxContextTokens int          // 0 = unknown
	DisableCaching   bool         // task 1.1
}
func New(opts Options) *Adapter

package openai // task 1.2
type Config struct {
	APIKey           string // "" = OPENAI_API_KEY
	BaseURL          string // "" = SDK default; set for OpenAI-compatible servers
	HTTPClient       *http.Client
	MaxContextTokens int
	RequestTimeout   time.Duration
}
func New(cfg Config) (*Adapter, error)
```

Model ids never appear in the repository: agent files use
`name: ${ADLC_ANTHROPIC_MODEL}` / `${ADLC_OPENAI_MODEL}` (§7), and live tests
(`//go:build live`) read the same variables.

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
`eventlog.Kind` are as in `internal/eventlog`. Task 1.11 adds:

```go
// Runs returns the ids of all runs in the store, sorted ascending.
func (s *Store) Runs(ctx context.Context) ([]string, error)
```

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

// Static is a Client over a fixed rule list. Build it with NewStatic.
type Static struct {
	Rules   []Rule
	Default adlcv1.Decision // used when no rule matches; must not be UNSPECIFIED
}

// NewStatic validates the rules (ids non-empty, unique, not "default";
// decisions set; def set) and returns a Static holding a copy of rules.
func NewStatic(rules []Rule, def adlcv1.Decision) (*Static, error)

// Names used in agent.yaml (task 0.9.1).
var ErrUnknownName = errors.New("policy: unknown name")
func ParseRiskClass(s string) (adlcv1.RiskClass, error) // read, write_local, write_external, exec, irreversible
func RiskClassName(r adlcv1.RiskClass) string
func ParseDecision(s string) (adlcv1.Decision, error)   // allow, deny, needs_approval
func DecisionName(d adlcv1.Decision) string
```

`Static.Authorize` denies any request whose `Risk` is UNSPECIFIED (rule id
`unspecified-risk`); first matching rule wins; otherwise `Default` with rule
id `default`.

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

// ValidateTool checks name pattern, risk (not UNSPECIFIED), server, and that
// InputSchema is a JSON object (empty allowed).
func ValidateTool(t Tool) error

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

**Every `Router` guarantees** that each tool in `Tools()` passed
`ValidateTool`, so its `Risk` is never UNSPECIFIED.

### §5.1 MCP router — `internal/toolrouter/mcprouter` (task 0.7.2)

```go
type ServerConfig struct {
	Name        string                      // ^[a-z][a-z0-9_]{0,15}$, no "__"
	Command     []string                    // argv; Command[0] is the program
	Env         map[string]string
	Dir         string
	Risk        map[string]adlcv1.RiskClass // tool name (not namespaced) -> risk
	DefaultRisk adlcv1.RiskClass            // for tools not in Risk; UNSPECIFIED = none
}
func (c ServerConfig) Validate() error

// Conn is a server plus an already-built transport.
type Conn struct {
	Config    ServerConfig
	Transport mcp.Transport
}

// Start validates each config, launches it as a subprocess and calls Connect.
func Start(ctx context.Context, servers []ServerConfig) (*Router, error)
// Connect connects over the given transports. It does not run Command.
func Connect(ctx context.Context, conns []Conn) (*Router, error)
```

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
	// Prices per model id (task 1.3). When the response's Model is in the
	// map, that price is used; otherwise Price.
	Prices map[string]model.Price
	// Context limits the size of each model request (task 1.6.1).
	// MaxInputTokens 0 = derived from the adapter's MaxContextTokens.
	Context contextmgr.Budget
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
// each request, and by resume in task 1.7). Tool outputs are capped with
// toolrouter.ForModel(output, maxToolOutputBytes) exactly as the model saw
// them; maxToolOutputBytes <= 0 means toolrouter.DefaultMaxModelBytes.
func Transcript(events []*adlcv1.Event, maxToolOutputBytes int) ([]model.Message, error)

// RetryPolicy controls retries of failed model calls (task 0.5.3).
type RetryPolicy struct {
	MaxAttempts  int           // total attempts incl. the first; 0 = 3; 1 = no retries
	InitialDelay time.Duration // wait after the first failed attempt; 0 = 1s
	MaxDelay     time.Duration // cap on every wait; 0 = 30s
}
// Delay returns min(InitialDelay * 2^(attempt-1), MaxDelay), defaults applied.
func (p RetryPolicy) Delay(attempt int) time.Duration

// HookDecision is a PreTool hook's answer (task 0.5.4).
type HookDecision struct {
	Deny   bool
	Reason string // required when Deny is true
}

// Hook observes and may block tool calls. Hooks run in Config.Hooks order.
type Hook interface {
	Name() string // ^[a-z0-9-]{1,40}$, unique within Config.Hooks
	// PreTool runs after tool_call_requested and before the policy check.
	PreTool(ctx context.Context, call model.ToolCall, tool toolrouter.Tool) (HookDecision, error)
	// PostTool runs after tool_call_completed. It cannot change the result.
	PostTool(ctx context.Context, call model.ToolCall, res toolrouter.Result) error
}

// VerifyCheckpoints folds events and checks that every checkpoint's
// state_digest equals the digest of the state just before it (task 0.5.4).
func VerifyCheckpoints(events []*adlcv1.Event) error

// Resume continues a run that stopped without a final run_completed (task 1.7).
func Resume(ctx context.Context, cfg Config, deps Deps) (*eventlog.RunState, error)
var (
	ErrRunFinished        = errors.New("harness: run already finished")
	ErrRunPaused          = errors.New("harness: run is waiting for approval")
	ErrCheckpointMismatch = errors.New("harness: checkpoint does not match the log")
	ErrAgentMismatch      = errors.New("harness: agent version differs from the run")
)
// AbandonedOutput / NotRunOutput: tool results Resume writes for calls that
// were running, or never started, when the run stopped (exact text: brief 1.7).
const AbandonedOutput = "abandoned: the run stopped while this tool call was running; it may or may not have taken effect. Check before repeating it."
const NotRunOutput = "not run: the run stopped before this tool call started. Call it again if it is still needed."
```

`Resume` ignores `cfg.Task` (the task is in `run_started`), only appends events
(first `run_resumed{next_step, verified_checkpoint_seq, abandoned_tool_calls,
reason}`, then the recovery events), and continues the same loop as `Run`.
It refuses finished runs (`ErrRunFinished`), paused runs (`ErrRunPaused`), a
different `AgentVersion` (`ErrAgentMismatch`) and a last checkpoint whose digest
does not match the log (`ErrCheckpointMismatch`); an unknown run wraps
`eventlog.ErrRunNotFound`. `run_resumed` does not change `RunState`.

Checkpoint digest: `checkpoint.state_digest` = `RunState.Digest()` of the
state folded from all events before the checkpoint (seq < checkpoint seq).
A checkpoint is written after every step whose tool calls all ran.

Budgets (task 0.5.2) are computed from the recorded events plus a
`startedAt` time, never from counters that start at zero, so a resumed run
keeps its budget (task 1.7).

From task 1.6.2 on, the harness sorts tools by name once per run and builds
every request through one `contextmgr.Manager` per run.

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
A run continued by `Resume` has one `run_resumed` (task 1.7) after the point
where the process stopped; it creates no message in `Transcript`.

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
| A PreTool hook denied (task 0.5.4) | `DENIED` (after `policy_decided` with `source = HOOK`, `rule_id = "hook:<name>"`) | `rule hook:<name>: <reason>` |
| A PreTool hook returned an error | `ERROR` | `hook <name>: <error>` |
| Policy client returned an error | `ERROR` | `policy: <error>` |
| Request too large for `Config.Context` (task 1.6.1) | `ERROR` | `context budget exceeded` |
| `ctx` cancelled | `CANCELLED` | |

Not a stop: an **unknown tool** (name not in `Tools()`) gets
`tool_call_requested` with risk UNSPECIFIED and an error `tool_call_completed`
that lists the available tools; the model sees it and the run continues.
Tool calls without an id get `m<step>-t<n>`; approval ids are `a-<tool call id>`.

When `ctx` is cancelled, the harness writes the final events with
`context.WithoutCancel(ctx)` so they are not lost.

## §7 Agent config — `internal/agentconfig` (task 0.9.1)

File format: brief 0.9.1 (providers `fake`, `anthropic`, `openai`;
`system_file` instead of `system`; `model.name` may be exactly `${VAR}`,
replaced from the environment; prices are decimal USD strings under
`model.pricing`). Go entry points:

```go
func Load(path string) (*Agent, error)          // read + parse + resolve + validate; sets Dir
func Parse(data []byte) (*Agent, error)         // same, Dir = ""
func (a *Agent) Validate() error                // all problems, joined with errors.Join
func (a *Agent) AgentID() string                // "<name>@<version>"
func (a *Agent) Price() model.Price
func (a *Agent) BudgetProto() *adlcv1.Budget    // nil if no limits
func (a *Agent) PolicyEngine() *policy.Static
func (a *Agent) ToolServers() []ParsedToolServer
func ParseMicroUSD(s string) (int64, error)
```

Task 1.6.1 adds the optional `context:` section (`max_input_tokens`,
`keep_recent_steps`, both >= 0) and `(*Agent).ContextBudget() contextmgr.Budget`,
which the CLI wiring assigns to `harness.Config.Context`.

Task 1.3 adds `model.fallbacks` and `(*Agent).Prices() map[string]model.Price`.
A fallback must not set `max_tokens` or `effort` (validation rejects them): the harness sends the primary's values and the router passes them through.

CLI wiring (task 0.9.2, `internal/cli/wiring.go`, unexported, shared by
`adlc run`, `adlc resume` (1.7) and `adlc eval` (1.8)):
`buildModel(a, fakeScript) (model.Adapter, error)`,
`harnessConfig(a, runID, task) harness.Config`,
`toServerConfigs([]agentconfig.ParsedToolServer) ([]mcprouter.ServerConfig, error)`,
`buildRun(ctx, a, opts, runID, log) (harness.Config, harness.Deps, func() error, error)`.

## §8 Sandbox — `internal/sandbox` (task 1.4.1)

```go
package sandbox

type Spec struct {
	RunID        string            // required; stored as label adlc.run_id (task 1.4.1)
	Image        string            // required, e.g. "golang:1.26"
	WorkspaceDir string            // host dir copied into the container at /workspace
	CPUs         float64           // 0 = no limit
	MemoryBytes  int64             // 0 = no limit
	Network      bool              // false = no network (default)
	Env          map[string]string // the only environment the container gets
}
func (s Spec) Validate() error // ErrInvalidSpec

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

const (
	LabelManaged  = "adlc.managed" // value "true"
	LabelRunID    = "adlc.run_id"
	WorkspacePath = "/workspace"
	MaxOutputBytes = 8 << 20       // cap on ExecResult.Stdout and .Stderr each
	TruncatedMarker = "\n[output truncated by sandbox]\n"
	SandboxUID, SandboxGID = 65532, 65532 // task 1.4.2
	PidsLimit         = 512
	MaxWorkspaceBytes = 512 << 20
	DefaultHome       = "/tmp"
	SandboxIDPlaceholder = "{{sandbox_id}}" // task 1.5
)
var (
	ErrInvalidSpec = errors.New("sandbox: invalid spec")
	ErrNotFound    = errors.New("sandbox: not found")
)
func CapOutput(b []byte, max int) []byte
// ExpandArgs replaces SandboxIDPlaceholder in each arg with h.ID (task 1.5).
func ExpandArgs(args []string, h Handle) []string
```

Exec semantics: argv run in `WorkspacePath`; `timeout` > 0, on expiry the
command is killed and the call returns `TimedOut: true`; a non-zero exit is
not an error (the error return means "could not run at all").

Defaults (task 1.4.2): no network unless `Network`; user 65532:65532; all
capabilities dropped; 512 processes; `HOME=/tmp` unless set; the workspace
is copied in, never bind-mounted.

Test double: `internal/sandbox/sandboxtest.Host` (`NewHost(dir)`) runs
commands on the host in a temp copy — no isolation, tests only.

Sandbox tools (task 1.5), used by `adlc eval`:

```go
package sandboxtool
type Options struct {
	Sandbox        sandbox.Sandbox
	Handle         sandbox.Handle
	DefaultTimeout time.Duration // 0 = 2m
	MaxTimeout     time.Duration // 0 = 10m
}

package sandboxset
// ServerConfigs returns the four server configs (shell, git, search, files),
// each with Command ["adlc", "tool", <name>, "--sandbox", sandboxID].
func ServerConfigs(sandboxID string) []mcprouter.ServerConfig
// NewRouter serves the four servers in process (in-memory MCP transports).
func NewRouter(ctx context.Context, o sandboxtool.Options) (*mcprouter.Router, error)
```

Tool names and risks: `shell__run` (EXEC), `git__status` (READ),
`git__diff` (READ), `search__grep` (READ), `files__read_file` (READ),
`files__write_file` (WRITE_LOCAL), `files__list_dir` (READ). Subprocess
form: `adlc tool shell|git|search|files --sandbox <id>`.

## §9 Evals — `internal/evals` (tasks E.3, 1.8, 1.9)

Layout (canonical; brief E.3 owns it):

```
evals/fixtures/<fixture>/          Go module, stdlib only (E.1: ledger, module example.com/ledger)
evals/suites/go.mod                module example.com/evalsuites (keeps hidden tests out of the main module)
evals/suites/go-bugs/<bug-id>/
  meta.yaml  issue.md  bug.patch  fix.patch  hidden_test.go
```

`meta.yaml` (top-level keys unindented, lists as block lists):

```yaml
id: money-split-remainder        # = directory name, ^[a-z0-9][a-z0-9-]*$
difficulty: easy                 # easy | medium | hard
title: Splitting an amount loses a cent
fixture: ledger                  # directory under evals/fixtures
test_package: money              # fixture dir of the hidden test ("." = root)
hidden_tests:                    # each ^TestHidden[A-Za-z0-9_]*$
  - TestHiddenSplitRemainder
timeout: 2m                      # 1s..10m
fix_files:
  - money/money.go
```

Patches are `git diff --relative` made inside the fixture (paths relative to
the fixture root), applied with `git apply`. The hidden test is always copied
to `<test_package>/adlc_hidden_test.go`.

```go
package evals
type Difficulty string // Easy, Medium, Hard
type Meta struct {
	ID, Title, Fixture, TestPackage string
	Difficulty  Difficulty
	HiddenTests []string
	Timeout     time.Duration
	FixFiles    []string
}
type Bug struct {
	Meta
	Dir, Issue string
	BugPatch, FixPatch, HiddenTest []byte
}
type Fixture struct{ Name, Dir string }
type Suite struct {
	Name, Dir string
	Fixtures  map[string]Fixture
	Bugs      []Bug // sorted by ID
}
var ErrInvalidSuite = errors.New("evals: invalid suite")
func LoadSuite(dir string) (*Suite, error)
func (s *Suite) Bug(id string) (Bug, bool)
type PrepareOptions struct{ NoBug, WithFix, WithHiddenTest bool }
func (s *Suite) PrepareWorkspace(ctx context.Context, bug Bug, dst string, opts PrepareOptions) error
func HiddenTestPath(bug Bug) string
func HiddenTestArgs(bug Bug) []string
type TestOutcome struct{ Passed, Failed, Missing []string; ExitCode int; TimedOut bool }
func ParseTestOutput(bug Bug, output []byte, exitCode int, timedOut bool) TestOutcome
const DefaultImage = "golang:1.26"
// task 1.8: AgentRun, Runner (RunOne, RunAll), Result, Report, WriteReport, PrintTable
// task 1.9: Check, Grade, Bucket, ProviderSummary, Summary, Summarize
```

Exact field lists: briefs E.3, 1.8 and 1.9. Eval results are stored in the
JSON report (with run ids), not as events.

## §10 Planned schema additions

| Task | Message | Field | Number |
|------|---------|-------|--------|
| 1.3 | `ModelCallCompleted` | `string provider` | 8 |
| 1.3 | `ModelCallCompleted` | `string model` | 9 |
| 1.7 | `Event` payload oneof | `RunResumed run_resumed` | 22 |
| 0.11.1 | new file `plugin.proto` | `HookService`, `ToolCallInfo`, `PreToolRequest/Response`, `PostToolRequest/Response` | — |

Every addition is additive; `make proto-breaking` must pass. Before taking a
number, check this table and the `.proto` file; add your row here in the same PR.
