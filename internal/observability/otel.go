package observability

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/roman-ambinder/agentic-framework/observability"

// Export emits root and its children as OTel spans through tp's tracer
// "github.com/roman-ambinder/agentic-framework/observability", using each
// Span's own Start/End times, then calls tp.ForceFlush.
func Export(ctx context.Context, root *Span, tp *sdktrace.TracerProvider) error {
	tr := tp.Tracer(tracerName)
	last := lastTime(root)
	rootCtx, rootSpan := tr.Start(ctx, root.Name,
		trace.WithTimestamp(root.Start), trace.WithAttributes(root.Attrs...),
		trace.WithSpanKind(trace.SpanKindInternal))
	for _, c := range root.Children {
		_, span := tr.Start(rootCtx, c.Name,
			trace.WithTimestamp(c.Start), trace.WithAttributes(c.Attrs...),
			trace.WithSpanKind(trace.SpanKindInternal))
		finishSpan(span, c, last)
	}
	finishSpan(rootSpan, root, last)
	if err := tp.ForceFlush(ctx); err != nil {
		return fmt.Errorf("observability: flush: %w", err)
	}
	return nil
}

// finishSpan adds s's events and status to span and ends it.
func finishSpan(span trace.Span, s *Span, last time.Time) {
	for _, e := range s.Events {
		span.AddEvent(e.Name, trace.WithTimestamp(e.Time), trace.WithAttributes(e.Attrs...))
	}
	if s.Failed {
		span.SetStatus(codes.Error, s.Status)
	}
	end := s.End
	if end.IsZero() {
		end = last
	}
	span.End(trace.WithTimestamp(end))
}

// lastTime is the latest time anywhere in the tree; spans that never ended
// are closed there.
func lastTime(root *Span) time.Time {
	last := root.Start
	later := func(t time.Time) {
		if t.After(last) {
			last = t
		}
	}
	var walk func(s *Span)
	walk = func(s *Span) {
		later(s.Start)
		later(s.End)
		for _, e := range s.Events {
			later(e.Time)
		}
		for _, c := range s.Children {
			walk(c)
		}
	}
	walk(root)
	return last
}

// idGenerator derives trace and span IDs from the run id.
type idGenerator struct {
	runID string
	mu    sync.Mutex
	n     int
}

// NewIDGenerator returns an sdktrace.IDGenerator whose trace ID is the first
// 16 bytes of sha256(runID) and whose span IDs are the first 8 bytes of
// sha256(runID + "/" + n) for n = 1, 2, 3, ... in creation order, so the
// same run always exports the same IDs.
func NewIDGenerator(runID string) sdktrace.IDGenerator {
	return &idGenerator{runID: runID}
}

func (g *idGenerator) traceID() (id trace.TraceID) {
	sum := sha256.Sum256([]byte(g.runID))
	copy(id[:], sum[:16])
	return id
}

func (g *idGenerator) spanID() (id trace.SpanID) {
	g.mu.Lock()
	g.n++
	n := g.n
	g.mu.Unlock()
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", g.runID, n)))
	copy(id[:], sum[:8])
	return id
}

// NewIDs implements sdktrace.IDGenerator.
func (g *idGenerator) NewIDs(context.Context) (trace.TraceID, trace.SpanID) {
	return g.traceID(), g.spanID()
}

// NewSpanID implements sdktrace.IDGenerator.
func (g *idGenerator) NewSpanID(context.Context, trace.TraceID) trace.SpanID {
	return g.spanID()
}
