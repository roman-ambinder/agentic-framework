// Package harness runs an agent: it calls the model, runs the tool calls the
// model asks for after a policy check, feeds the results back, and stops with
// one of the stop reasons in docs/impl/interfaces.md §6.2. Every step is
// written to the event log in the order of §6.1, and the conversation for each
// model call is rebuilt from the log by Transcript.
package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/contextmgr"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// defaultMaxTokens is Config.MaxTokens when it is zero.
const defaultMaxTokens = 4096

// Log is the event log the harness writes to; *eventlog.Store implements it.
type Log interface {
	Append(ctx context.Context, ev *adlcv1.Event) (*adlcv1.Event, error)
	Read(ctx context.Context, runID string, afterSeq uint64) ([]*adlcv1.Event, error)
}

// Config is everything about one run that is not a dependency.
type Config struct {
	RunID              string
	AgentID            string // e.g. "hello@1"
	AgentVersion       string // same as AgentID for now
	Task               string // first user message
	System             string
	Model              string // passed to Request.Model
	MaxTokens          int    // per model call; 0 = 4096
	Effort             string
	Budget             *adlcv1.Budget
	Price              model.Price
	Prices             map[string]model.Price // per model id (task 1.3); Price when absent
	MaxToolOutputBytes int                    // 0 = toolrouter.DefaultMaxModelBytes
	Retry              RetryPolicy
	Hooks              []Hook
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
	Now    func() time.Time                                 // default time.Now
	Sleep  func(ctx context.Context, d time.Duration) error // default: timer + ctx
	// KillSwitch, if set, is asked before every model call and every tool
	// call whether the run must stop (task C.3). nil = no kill switch.
	KillSwitch KillSwitch
}

// Run executes a new run until it stops and returns its final state.
// The error is non-nil only for failures to use the event log (or an invalid
// configuration, which writes nothing); every other failure ends the run with
// a stop reason recorded in the log.
func Run(ctx context.Context, cfg Config, deps Deps) (*eventlog.RunState, error) {
	cfg, deps = applyDefaults(cfg, deps)
	if cfg.Task == "" {
		return nil, errors.New("harness: config: task is empty")
	}
	if err := validate(cfg, deps); err != nil {
		return nil, err
	}
	cfg.Context = effectiveBudget(cfg.Context, deps.Model.Capabilities())
	return newLoop(ctx, cfg, deps).run()
}

// effectiveBudget fills in MaxInputTokens from the adapter's context window
// (80 %) when the config leaves it at zero and the window is known.
func effectiveBudget(b contextmgr.Budget, caps model.Caps) contextmgr.Budget {
	if b.MaxInputTokens == 0 && caps.MaxContextTokens > 0 {
		b.MaxInputTokens = int64(caps.MaxContextTokens) * 8 / 10
	}
	return b
}

func applyDefaults(cfg Config, deps Deps) (Config, Deps) {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Sleep == nil {
		deps.Sleep = sleepCtx
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	if cfg.MaxToolOutputBytes == 0 {
		cfg.MaxToolOutputBytes = toolrouter.DefaultMaxModelBytes
	}
	return cfg, deps
}

func validate(cfg Config, deps Deps) error {
	switch {
	case cfg.RunID == "":
		return errors.New("harness: config: run id is empty")
	case deps.Log == nil:
		return errors.New("harness: deps: log is nil")
	case deps.Model == nil:
		return errors.New("harness: deps: model is nil")
	case deps.Tools == nil:
		return errors.New("harness: deps: tools is nil")
	case deps.Policy == nil:
		return errors.New("harness: deps: policy is nil")
	}
	if err := cfg.Context.Validate(); err != nil {
		return fmt.Errorf("harness: config: %w", err)
	}
	return validateHooks(cfg.Hooks)
}

// sleepCtx waits for d, or returns ctx.Err() if ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
