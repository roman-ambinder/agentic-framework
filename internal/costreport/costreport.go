// Package costreport groups the cost recorded on event log events into a
// report. It only reads events: every number is the sum of the cost fields
// that were written when the events were recorded, so a report's total always
// equals the sum of the events' costs. Prices are never applied here.
package costreport

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// By is how rows are grouped.
type By string

// Groupings accepted by Build.
const (
	ByRun   By = "run"
	ByModel By = "model"
	ByAgent By = "agent"
)

// Keys used for rows that do not belong to a specific run, model or agent.
const (
	keyOther   = "(other)"
	keyUnknown = "(unknown)"
	keyTotal   = "TOTAL"
)

// Row is one line of the report. Key is the run id, "<provider>/<model>",
// or the agent id, depending on By.
type Row struct {
	Key              string `json:"key"`
	Runs             int    `json:"runs"`        // number of distinct runs contributing
	ModelCalls       int    `json:"model_calls"` // number of model_call_completed events
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	USDMicros        int64  `json:"usd_micros"`
}

// Report is the whole result. Rows are sorted by Key ascending.
type Report struct {
	By    By    `json:"by"`
	Rows  []Row `json:"rows"`
	Total Row   `json:"total"` // Key "TOTAL"; Runs = distinct runs overall
}

// acc accumulates one row while the events are walked.
type acc struct {
	row  Row
	seen map[string]struct{}
}

// builder holds the accumulators for one Build call.
type builder struct {
	by    By
	rows  map[string]*acc
	total *acc
}

// Build groups the cost of the given runs. runs maps run id → its events
// (seq order). It returns an error for an unknown By value.
func Build(by By, runs map[string][]*adlcv1.Event) (Report, error) {
	switch by {
	case ByRun, ByModel, ByAgent:
	default:
		return Report{}, fmt.Errorf("costreport: unknown grouping %q (want run, model or agent)", by)
	}
	b := &builder{by: by, rows: map[string]*acc{}, total: newAcc(keyTotal)}
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		b.addRun(id, runs[id])
	}
	b.total.row.Runs = len(runs)
	return b.report(), nil
}

func newAcc(key string) *acc {
	return &acc{row: Row{Key: key}, seen: map[string]struct{}{}}
}

// addRun walks one run's events in order and adds their cost to the rows.
func (b *builder) addRun(runID string, events []*adlcv1.Event) {
	modelOf := map[string]string{}
	agent := agentOf(events)
	for _, ev := range events {
		if started := ev.GetModelCallStarted(); started != nil {
			modelOf[started.GetCallId()] = started.GetProvider() + "/" + started.GetModel()
		}
		key := b.key(runID, agent, ev, modelOf)
		if c := ev.GetCost(); c != nil {
			b.add(key, runID, c)
		}
		if ev.GetModelCallCompleted() != nil {
			b.count(key, runID)
		}
	}
}

// key returns the row an event's cost belongs to.
func (b *builder) key(runID, agent string, ev *adlcv1.Event, modelOf map[string]string) string {
	switch b.by {
	case ByRun:
		return runID
	case ByAgent:
		return agent
	}
	var callID string
	switch {
	case ev.GetModelCallCompleted() != nil:
		callID = ev.GetModelCallCompleted().GetCallId()
	case ev.GetModelCallFailed() != nil:
		callID = ev.GetModelCallFailed().GetCallId()
	default:
		return keyOther
	}
	if m, ok := modelOf[callID]; ok {
		return m
	}
	return keyOther
}

// agentOf returns the agent id of a run, taken from its run_started event.
func agentOf(events []*adlcv1.Event) string {
	for _, ev := range events {
		if ev.GetRunStarted() != nil && ev.GetAgentId() != "" {
			return ev.GetAgentId()
		}
	}
	return keyUnknown
}

// row returns the accumulator for key, creating it on first use.
func (b *builder) row(key string) *acc {
	a, ok := b.rows[key]
	if !ok {
		a = newAcc(key)
		b.rows[key] = a
	}
	return a
}

// add adds one event's cost to the row for key and to the total.
func (b *builder) add(key, runID string, c *adlcv1.Cost) {
	for _, a := range []*acc{b.row(key), b.total} {
		a.row.InputTokens += c.GetInputTokens()
		a.row.OutputTokens += c.GetOutputTokens()
		a.row.CacheReadTokens += c.GetCacheReadTokens()
		a.row.CacheWriteTokens += c.GetCacheWriteTokens()
		a.row.USDMicros += c.GetUsdMicros()
		a.seen[runID] = struct{}{}
	}
}

// count counts one model call for key and in the total.
func (b *builder) count(key, runID string) {
	for _, a := range []*acc{b.row(key), b.total} {
		a.row.ModelCalls++
		a.seen[runID] = struct{}{}
	}
}

// report turns the accumulators into a sorted Report. Rows with nothing in
// them are dropped.
func (b *builder) report() Report {
	rows := []Row{}
	for _, a := range b.rows {
		if a.row.InputTokens == 0 && a.row.OutputTokens == 0 && a.row.CacheReadTokens == 0 &&
			a.row.CacheWriteTokens == 0 && a.row.USDMicros == 0 && a.row.ModelCalls == 0 {
			continue
		}
		a.row.Runs = len(a.seen)
		rows = append(rows, a.row)
	}
	slices.SortFunc(rows, func(x, y Row) int { return strings.Compare(x.Key, y.Key) })
	return Report{By: b.by, Rows: rows, Total: b.total.row}
}

// WriteText writes the report as an aligned table.
func WriteText(w io.Writer, r Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\tRUNS\tCALLS\tINPUT\tOUTPUT\tCACHE_READ\tCACHE_WRITE\tUSD\t\n",
		strings.ToUpper(string(r.By)))
	for _, row := range r.Rows {
		writeRow(tw, row)
	}
	writeRow(tw, r.Total)
	return tw.Flush()
}

// writeRow writes one report line into the tabwriter.
func writeRow(tw io.Writer, r Row) {
	fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t\n", r.Key, r.Runs, r.ModelCalls,
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, formatUSD(r.USDMicros))
}

// formatUSD formats micro-USD as dollars with six decimals.
func formatUSD(m int64) string {
	sign := ""
	if m < 0 {
		sign, m = "-", -m
	}
	return fmt.Sprintf("%s%d.%06d", sign, m/1_000_000, m%1_000_000)
}

// WriteJSON writes the report as indented JSON (two spaces) and a newline.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
