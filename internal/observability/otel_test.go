package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func export(t *testing.T, root *Span, runID string) tracetest.SpanStubs {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp), sdktrace.WithIDGenerator(NewIDGenerator(runID)))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	if err := Export(context.Background(), root, tp); err != nil {
		t.Fatal(err)
	}
	return exp.GetSpans()
}

func TestExportSpans(t *testing.T) {
	root, err := Build(fullRun())
	if err != nil {
		t.Fatal(err)
	}
	spans := export(t, root, "r1")
	if len(spans) != 4 {
		t.Fatalf("spans = %d, want 4", len(spans))
	}
	byName := map[string]tracetest.SpanStub{}
	for _, s := range spans {
		byName[s.Name] = s
	}
	rs := byName["run r1"]
	if !rs.StartTime.Equal(t0) || !rs.EndTime.Equal(t0.Add(6*sec)) {
		t.Errorf("root times = %v..%v", rs.StartTime, rs.EndTime)
	}
	for _, c := range root.Children {
		s, ok := byName[c.Name]
		if !ok {
			t.Fatalf("span %q not exported", c.Name)
		}
		if s.Parent.SpanID() != rs.SpanContext.SpanID() {
			t.Errorf("%s parent = %v, want root %v", c.Name, s.Parent.SpanID(), rs.SpanContext.SpanID())
		}
		if !s.StartTime.Equal(c.Start) || !s.EndTime.Equal(c.End) {
			t.Errorf("%s times = %v..%v, want %v..%v", c.Name, s.StartTime, s.EndTime, c.Start, c.End)
		}
	}
	var found bool
	for _, a := range byName["model m1"].Attributes {
		if string(a.Key) == "adlc.output_tokens" {
			found = a.Value.AsInt64() == 20
		}
	}
	if !found {
		t.Errorf("m1 lacks adlc.output_tokens=20: %v", byName["model m1"].Attributes)
	}
	if ev := byName["tool t1 fs__read_file"].Events; len(ev) != 1 || ev[0].Name != "policy" {
		t.Errorf("t1 events = %v", ev)
	}
}

func TestExportDeterministicIDs(t *testing.T) {
	root, err := Build(fullRun())
	if err != nil {
		t.Fatal(err)
	}
	a, b := export(t, root, "r1"), export(t, root, "r1")
	for i := range a {
		if a[i].SpanContext.TraceID() != b[i].SpanContext.TraceID() || a[i].SpanContext.SpanID() != b[i].SpanContext.SpanID() {
			t.Errorf("span %d ids differ between exports", i)
		}
	}
	if c := export(t, root, "r2"); c[0].SpanContext.TraceID() == a[0].SpanContext.TraceID() {
		t.Errorf("different runs share a trace id")
	}
}

func TestExportFailedSpanStatus(t *testing.T) {
	root, err := Build(numbered(runStarted(), modelStarted(1*sec, "m1", 1), modelFailed(2*sec, "m1", false)))
	if err != nil {
		t.Fatal(err)
	}
	spans := export(t, root, "r1")
	for _, s := range spans {
		if s.Name == "model m1" && s.Status.Code != codes.Error {
			t.Errorf("status = %v, want Error", s.Status.Code)
		}
	}
	// The unfinished root is closed at the last event time, not time.Now().
	for _, s := range spans {
		if s.Name == "run r1" && !s.EndTime.Equal(t0.Add(2*sec)) {
			t.Errorf("root end = %v, want %v", s.EndTime, t0.Add(2*sec))
		}
	}
}
