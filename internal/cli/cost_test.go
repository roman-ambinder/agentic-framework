package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/costreport"
	"github.com/roman-ambinder/agentic-framework/internal/eventlog"
)

// costDB writes a log with two runs, r1 and r2, each with one priced model
// call, and returns its path.
func costDB(t *testing.T) string {
	t.Helper()
	db := filepath.Join(t.TempDir(), "events.db")
	store, err := eventlog.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, r := range []struct {
		id    string
		usd   int64
		input int64
	}{{"r1", 750, 150}, {"r2", 40, 10}} {
		events := []*adlcv1.Event{
			{RunId: r.id, AgentId: "coder@1", Payload: &adlcv1.Event_RunStarted{
				RunStarted: &adlcv1.RunStarted{AgentVersion: "coder@1", Task: "t"},
			}},
			{
				RunId: r.id, Cost: &adlcv1.Cost{InputTokens: r.input, OutputTokens: 1, UsdMicros: r.usd},
				Payload: &adlcv1.Event_ModelCallCompleted{ModelCallCompleted: &adlcv1.ModelCallCompleted{CallId: "m1"}},
			},
		}
		for _, ev := range events {
			if _, err := store.Append(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func TestCostAllRuns(t *testing.T) {
	db := costDB(t)
	code, out, errOut := run(t, "cost", "--db", db)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"RUN", "r1", "r2", "TOTAL", "0.000790"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCostSelectedRunJSON(t *testing.T) {
	db := costDB(t)
	code, out, errOut := run(t, "cost", "r1", "--json", "--db", db)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var rep costreport.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].Key != "r1" || rep.Rows[0].USDMicros != 750 {
		t.Errorf("rows = %+v", rep.Rows)
	}
}

func TestCostUnknownRun(t *testing.T) {
	db := costDB(t)
	code, out, errOut := run(t, "cost", "nope", "--db", db)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut, "not found") {
		t.Errorf("stderr %q does not contain not found", errOut)
	}
	if out != "" {
		t.Errorf("stdout %q, want empty", out)
	}
}

func TestCostBadGrouping(t *testing.T) {
	db := costDB(t)
	code, _, errOut := run(t, "cost", "--by", "day", "--db", db)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut, "unknown grouping") {
		t.Errorf("stderr %q", errOut)
	}
}

func TestCostEmptyLog(t *testing.T) {
	db := filepath.Join(t.TempDir(), "empty.db")
	code, out, errOut := run(t, "cost", "--db", db)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.HasPrefix(out, "RUN") || !strings.Contains(out, "TOTAL") {
		t.Errorf("output = %q", out)
	}
	code, out, _ = run(t, "cost", "--json", "--db", db)
	if code != 0 || !strings.Contains(out, `"rows": []`) {
		t.Errorf("json exit %d, output %q", code, out)
	}
}
