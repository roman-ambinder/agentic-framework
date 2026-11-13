package costreport

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// cost builds a Cost with the given input/output tokens and micro-USD.
func cost(in, out, usd int64) *adlcv1.Cost {
	return &adlcv1.Cost{InputTokens: in, OutputTokens: out, UsdMicros: usd}
}

func withCache(c *adlcv1.Cost, read, write int64) *adlcv1.Cost {
	c.CacheReadTokens, c.CacheWriteTokens = read, write
	return c
}

// ev builds an event of run runID with the given payload and cost.
func ev(runID string, p isPayload, c *adlcv1.Cost) *adlcv1.Event {
	e := &adlcv1.Event{RunId: runID, Cost: c}
	p.set(e)
	return e
}

type isPayload interface{ set(*adlcv1.Event) }

type (
	startedP      struct{ agent string }
	modelStartedP struct{ call, provider, model string }
	completedP    struct{ call string }
	failedP       struct{ call string }
	errorP        struct{}
)

func (p startedP) set(e *adlcv1.Event) {
	e.AgentId = p.agent
	e.Payload = &adlcv1.Event_RunStarted{RunStarted: &adlcv1.RunStarted{AgentVersion: p.agent, Task: "t"}}
}

func (p modelStartedP) set(e *adlcv1.Event) {
	e.Payload = &adlcv1.Event_ModelCallStarted{ModelCallStarted: &adlcv1.ModelCallStarted{
		CallId: p.call, Provider: p.provider, Model: p.model, Attempt: 1,
	}}
}

func (p completedP) set(e *adlcv1.Event) {
	e.Payload = &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: &adlcv1.ModelCallCompleted{CallId: p.call}}
}

func (p failedP) set(e *adlcv1.Event) {
	e.Payload = &adlcv1.Event_ModelCallFailed{ModelCallFailed: &adlcv1.ModelCallFailed{CallId: p.call}}
}

func (errorP) set(e *adlcv1.Event) {
	e.Payload = &adlcv1.Event_ErrorOccurred{ErrorOccurred: &adlcv1.ErrorOccurred{Message: "x"}}
}

// fixtureByRun returns two runs whose sums are easy to check by hand.
//
//	r1: two completed calls on fake/a: input 100+50, output 20+10, usd 500+250
//	r2: one completed call on fake/a: input 10, output 5, cache read 80, usd 40
func fixtureByRun() map[string][]*adlcv1.Event {
	return map[string][]*adlcv1.Event{
		"r1": {
			ev("r1", startedP{"coder@1"}, nil),
			ev("r1", modelStartedP{"m1", "fake", "a"}, nil),
			ev("r1", completedP{"m1"}, cost(100, 20, 500)),
			ev("r1", modelStartedP{"m2", "fake", "a"}, nil),
			ev("r1", completedP{"m2"}, cost(50, 10, 250)),
		},
		"r2": {
			ev("r2", startedP{"coder@1"}, nil),
			ev("r2", modelStartedP{"m1", "fake", "a"}, nil),
			ev("r2", completedP{"m1"}, withCache(cost(10, 5, 40), 80, 0)),
		},
	}
}

func row(t *testing.T, r Report, key string) Row {
	t.Helper()
	for _, x := range r.Rows {
		if x.Key == key {
			return x
		}
	}
	t.Fatalf("no row %q in %+v", key, r.Rows)
	return Row{}
}

func TestBuildByRun(t *testing.T) {
	r, err := Build(ByRun, fixtureByRun())
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{Key: "r1", Runs: 1, ModelCalls: 2, InputTokens: 150, OutputTokens: 30, USDMicros: 750},
		{Key: "r2", Runs: 1, ModelCalls: 1, InputTokens: 10, OutputTokens: 5, CacheReadTokens: 80, USDMicros: 40},
	}
	if !reflect.DeepEqual(r.Rows, want) {
		t.Errorf("rows = %+v\nwant %+v", r.Rows, want)
	}
	wantTotal := Row{Key: "TOTAL", Runs: 2, ModelCalls: 3, InputTokens: 160, OutputTokens: 35, CacheReadTokens: 80, USDMicros: 790}
	if r.Total != wantTotal {
		t.Errorf("total = %+v, want %+v", r.Total, wantTotal)
	}
}

func TestBuildByModel(t *testing.T) {
	events := []*adlcv1.Event{
		ev("r1", startedP{"coder@1"}, nil),
		ev("r1", modelStartedP{"m1", "fake", "a"}, nil),
		ev("r1", completedP{"m1"}, cost(100, 0, 500)),
		ev("r1", modelStartedP{"m2", "fake", "a"}, nil),
		ev("r1", completedP{"m2"}, cost(100, 0, 500)),
		ev("r1", modelStartedP{"m3", "fake", "b"}, nil),
		ev("r1", completedP{"m3"}, cost(50, 0, 250)),
		ev("r1", errorP{}, cost(0, 0, 7)),
	}
	r, err := Build(ByModel, map[string][]*adlcv1.Event{"r1": events})
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, x := range r.Rows {
		keys = append(keys, x.Key)
	}
	if want := []string{"(other)", "fake/a", "fake/b"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	other, a, b := row(t, r, "(other)"), row(t, r, "fake/a"), row(t, r, "fake/b")
	if other.ModelCalls != 0 || other.USDMicros != 7 {
		t.Errorf("(other) = %+v", other)
	}
	if a.ModelCalls != 2 || a.InputTokens != 200 || a.USDMicros != 1000 {
		t.Errorf("fake/a = %+v", a)
	}
	if b.ModelCalls != 1 || b.InputTokens != 50 || b.USDMicros != 250 {
		t.Errorf("fake/b = %+v", b)
	}
	if r.Total.USDMicros != 1257 || r.Total.ModelCalls != 3 {
		t.Errorf("total = %+v", r.Total)
	}
}

func TestBuildByAgent(t *testing.T) {
	runs := map[string][]*adlcv1.Event{
		"r1": {ev("r1", startedP{"coder@1"}, nil), ev("r1", completedP{"m1"}, cost(1, 1, 10))},
		"r2": {ev("r2", startedP{"coder@1"}, nil), ev("r2", completedP{"m1"}, cost(2, 2, 20))},
		"r3": {ev("r3", startedP{"coder@2"}, nil), ev("r3", completedP{"m1"}, cost(4, 4, 40))},
	}
	r, err := Build(ByAgent, runs)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 2 {
		t.Fatalf("rows = %+v, want 2", r.Rows)
	}
	if got := row(t, r, "coder@1"); got.Runs != 2 || got.USDMicros != 30 {
		t.Errorf("coder@1 = %+v", got)
	}
	if got := row(t, r, "coder@2"); got.Runs != 1 || got.USDMicros != 40 {
		t.Errorf("coder@2 = %+v", got)
	}
}

func TestBuildRetriedCallSameModel(t *testing.T) {
	events := []*adlcv1.Event{
		ev("r1", startedP{"coder@1"}, nil),
		ev("r1", modelStartedP{"m1", "fake", "a"}, nil),
		ev("r1", failedP{"m1"}, cost(0, 0, 30)),
		ev("r1", modelStartedP{"m1", "fake", "a"}, nil),
		ev("r1", completedP{"m1"}, cost(0, 0, 70)),
	}
	r, err := Build(ByModel, map[string][]*adlcv1.Event{"r1": events})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 1 {
		t.Fatalf("rows = %+v, want one row", r.Rows)
	}
	if got := r.Rows[0]; got.Key != "fake/a" || got.USDMicros != 100 || got.ModelCalls != 1 {
		t.Errorf("row = %+v", got)
	}
}

func TestTotalMatchesEventSum(t *testing.T) {
	runs := map[string][]*adlcv1.Event{}
	var want Row // plain sums; a protobuf Cost must not be copied
	for i := 0; i < 50; i++ {
		runID := []string{"r1", "r2", "r3"}[i%3]
		c := withCache(cost(int64(i*7), int64(i*3+1), int64(i*11)), int64(i), int64(i%4))
		want.InputTokens += c.GetInputTokens()
		want.OutputTokens += c.GetOutputTokens()
		want.CacheReadTokens += c.GetCacheReadTokens()
		want.CacheWriteTokens += c.GetCacheWriteTokens()
		want.USDMicros += c.GetUsdMicros()
		var p isPayload = completedP{"m" + string(rune('a'+i%5))}
		if i%2 == 0 {
			p = errorP{}
		}
		if i%5 == 0 {
			p = startedP{"coder@1"}
		}
		runs[runID] = append(runs[runID], ev(runID, p, c))
	}
	for _, by := range []By{ByRun, ByModel, ByAgent} {
		r, err := Build(by, runs)
		if err != nil {
			t.Fatal(err)
		}
		got := r.Total
		if got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens ||
			got.CacheReadTokens != want.CacheReadTokens || got.CacheWriteTokens != want.CacheWriteTokens ||
			got.USDMicros != want.USDMicros {
			t.Errorf("by %s: total %+v, want event sums %+v", by, got, want)
		}
		var sum int64
		for _, x := range r.Rows {
			sum += x.USDMicros
		}
		if sum != want.USDMicros {
			t.Errorf("by %s: row sum %d, want %d", by, sum, want.USDMicros)
		}
	}
}

func TestBuildUnknownGrouping(t *testing.T) {
	_, err := Build(By("day"), fixtureByRun())
	if err == nil || !strings.Contains(err.Error(), "unknown grouping") {
		t.Fatalf("err = %v, want unknown grouping", err)
	}
}

const wantTextByRun = `RUN    RUNS  CALLS  INPUT  OUTPUT  CACHE_READ  CACHE_WRITE  USD       
r1     1     2      150    30      0           0            0.000750  
r2     1     1      10     5       80          0            0.000040  
TOTAL  2     3      160    35      80          0            0.000790  
`

func TestWriteTextGolden(t *testing.T) {
	r, err := Build(ByRun, fixtureByRun())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteText(&buf, r); err != nil {
		t.Fatal(err)
	}
	if buf.String() != wantTextByRun {
		t.Errorf("text output:\n%s\nwant:\n%s", buf.String(), wantTextByRun)
	}
}

func TestWriteJSON(t *testing.T) {
	r, err := Build(ByRun, fixtureByRun())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("output does not end with newline")
	}
	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(back, r) {
		t.Errorf("round trip = %+v, want %+v", back, r)
	}
}

func TestFormatUSD(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0.000000"},
		{"fraction", 750, "0.000750"},
		{"dollars", 1_234_567, "1.234567"},
		{"negative", -2_500_000, "-2.500000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUSD(tt.in); got != tt.want {
				t.Errorf("formatUSD(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
