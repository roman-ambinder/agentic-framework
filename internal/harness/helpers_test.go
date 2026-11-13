package harness

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// newLog opens an eventlog.Store in t.TempDir().
func newLog(t *testing.T) *eventlog.Store {
	t.Helper()
	s, err := eventlog.Open(context.Background(), filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// kinds returns eventlog.Kind of each event.
func kinds(evs []*adlcv1.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = eventlog.Kind(ev)
	}
	return out
}

// readTool is the static tool "fs__read_file" (risk READ) returning "contents of <path>".
func readTool() toolrouter.StaticTool {
	return toolrouter.StaticTool{
		Tool: toolrouter.Tool{
			Spec:   model.ToolSpec{Name: "fs__read_file", Description: "read a file"},
			Server: "fs",
			Risk:   adlcv1.RiskClass_RISK_CLASS_READ,
		},
		Fn: func(_ context.Context, args json.RawMessage) (toolrouter.Result, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return toolrouter.Result{}, err
			}
			return toolrouter.Result{Output: []byte("contents of " + a.Path)}, nil
		},
	}
}

// staticTools returns a router with fs__read_file plus extra tools.
func staticTools(t *testing.T, extra ...toolrouter.StaticTool) toolrouter.Router {
	t.Helper()
	r, err := toolrouter.NewStatic(append([]toolrouter.StaticTool{readTool()}, extra...)...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// funcTool is a tool with the given name and risk running fn.
func funcTool(name string, risk adlcv1.RiskClass, fn toolrouter.Func) toolrouter.StaticTool {
	return toolrouter.StaticTool{
		Tool: toolrouter.Tool{Spec: model.ToolSpec{Name: name}, Server: "x", Risk: risk},
		Fn:   fn,
	}
}

func textTurn(text string) model.FakeTurn {
	return model.FakeTurn{Response: &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.TextPart(text)}},
		Finish:  adlcv1.FinishReason_FINISH_REASON_END_TURN,
	}}
}

func toolTurn(calls ...model.ToolCall) model.FakeTurn {
	parts := make([]model.Part, len(calls))
	for i, c := range calls {
		parts[i] = model.ToolCallPart(c)
	}
	return model.FakeTurn{Response: &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Parts: parts},
		Finish:  adlcv1.FinishReason_FINISH_REASON_TOOL_USE,
	}}
}

func readCall(id, path string) model.ToolCall {
	return model.ToolCall{ID: id, Name: "fs__read_file", Arguments: json.RawMessage(`{"path":"` + path + `"}`)}
}

// baseConfig returns the config most tests use.
func baseConfig() Config {
	return Config{RunID: "r1", AgentID: "test@1", AgentVersion: "test@1", Task: "do it", Model: "m"}
}

// failingLog wraps a Log and fails Append after n successful calls.
type failingLog struct {
	Log
	mu sync.Mutex
	n  int
}

var errLogDown = errors.New("log down")

func (f *failingLog) Append(ctx context.Context, ev *adlcv1.Event) (*adlcv1.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == 0 {
		return nil, errLogDown
	}
	f.n--
	return f.Log.Append(ctx, ev)
}

// harnessRun is the result of runHarness.
type harnessRun struct {
	state  *eventlog.RunState
	events []*adlcv1.Event
	model  *model.Fake
}

// runHarness runs cfg against a fake model with the given turns, then checks
// that the log folds to the returned state.
func runHarness(t *testing.T, ctx context.Context, cfg Config, deps Deps, turns ...model.FakeTurn) harnessRun {
	t.Helper()
	store := newLog(t)
	fake := model.NewFake(turns...)
	deps.Log, deps.Model = store, fake
	state, err := Run(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return harnessRun{state: state, events: assertFolds(t, store, cfg.RunID, state), model: fake}
}

// assertFolds reads the run's log, folds it and checks the result equals state.
func assertFolds(t *testing.T, log Log, runID string, state *eventlog.RunState) []*adlcv1.Event {
	t.Helper()
	events, err := log.Read(context.Background(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	folded, err := eventlog.Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	want, err1 := folded.Digest()
	got, err2 := state.Digest()
	if err1 != nil || err2 != nil {
		t.Fatalf("digest: %v %v", err1, err2)
	}
	if got != want {
		t.Fatalf("returned state differs from folded log:\n got %+v\nwant %+v", state, folded)
	}
	return events
}

// pick returns the payloads of one kind, in order, e.g.
// pick(evs, (*adlcv1.Event).GetToolCallCompleted).
func pick[T any](evs []*adlcv1.Event, get func(*adlcv1.Event) *T) []*T {
	var out []*T
	for _, ev := range evs {
		if v := get(ev); v != nil {
			out = append(out, v)
		}
	}
	return out
}
