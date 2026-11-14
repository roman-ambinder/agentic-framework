package observability

import (
	"fmt"
	"io"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

// WriteTree writes root as an indented tree. Output is deterministic:
// no maps are iterated, times are shown as offsets from root.Start.
func WriteTree(w io.Writer, root *Span) error {
	var sb strings.Builder
	writeSpanLine(&sb, "", root, root)
	writeImageLine(&sb, root)
	for _, e := range root.Events {
		writeEventLine(&sb, "  ", e, root)
	}
	for i, c := range root.Children {
		prefix, eprefix := "├─ ", "│    "
		if i == len(root.Children)-1 {
			prefix, eprefix = "└─ ", "     "
		}
		writeSpanLine(&sb, prefix, c, root)
		for _, e := range c.Events {
			writeEventLine(&sb, eprefix, e, root)
		}
	}
	if _, err := io.WriteString(w, sb.String()); err != nil {
		return fmt.Errorf("observability: write tree: %w", err)
	}
	return nil
}

func writeSpanLine(sb *strings.Builder, prefix string, s, root *Span) {
	dur := "-"
	if !s.End.IsZero() {
		dur = formatDur(s.End.Sub(s.Start))
	}
	fmt.Fprintf(sb, "%s%s  [%s]  +%s  %s  %s\n",
		prefix, s.Name, s.Status, formatDur(s.Start.Sub(root.Start)), dur, details(s))
}

// writeImageLine prints the sandbox image of a run, when it recorded one.
func writeImageLine(sb *strings.Builder, root *Span) {
	ref := attrString(root.Attrs, "adlc.image_ref")
	if ref == "" {
		return
	}
	if id := attrString(root.Attrs, "adlc.image_id"); id != "" {
		fmt.Fprintf(sb, "  image: %s (%s)\n", ref, sandbox.ShortImageID(id))
		return
	}
	fmt.Fprintf(sb, "  image: %s\n", ref)
}

func writeEventLine(sb *strings.Builder, eprefix string, e SpanEvent, root *Span) {
	fmt.Fprintf(sb, "%s· %s +%s", eprefix, e.Name, formatDur(e.Time.Sub(root.Start)))
	for _, a := range e.Attrs {
		fmt.Fprintf(sb, " %s=%s", a.Key, a.Value.String())
	}
	sb.WriteByte('\n')
}

func formatDur(d time.Duration) string { return d.Round(time.Millisecond).String() }

func formatUSD(micros int64) string {
	return fmt.Sprintf("%d.%06d", micros/1e6, micros%1e6)
}

func details(s *Span) string {
	switch s.Kind {
	case KindModel:
		return fmt.Sprintf("%s/%s in=%d out=%d $%s",
			attrString(s.Attrs, "adlc.provider"), attrString(s.Attrs, "adlc.model"),
			attrInt(s.Attrs, "adlc.input_tokens"), attrInt(s.Attrs, "adlc.output_tokens"),
			formatUSD(attrInt(s.Attrs, "adlc.usd_micros")))
	case KindTool:
		decision := "-"
		for _, e := range s.Events {
			if e.Name == "policy" {
				decision = short(attrString(e.Attrs, "adlc.decision"), "DECISION_")
			}
		}
		return short(attrString(s.Attrs, "adlc.risk_class"), "RISK_CLASS_") + " " + decision
	default:
		return fmt.Sprintf("in=%d out=%d $%s",
			attrInt(s.Attrs, "adlc.input_tokens"), attrInt(s.Attrs, "adlc.output_tokens"),
			formatUSD(attrInt(s.Attrs, "adlc.usd_micros")))
	}
}

func attrString(attrs []attribute.KeyValue, key string) string {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value.String()
		}
	}
	return ""
}

func attrInt(attrs []attribute.KeyValue, key string) int64 {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value.AsInt64()
		}
	}
	return 0
}
