package evals

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// cres builds a graded result of agent on bug.
func cres(agent, bug string, d Difficulty, solved bool) Result {
	return Result{
		BugID: bug, Difficulty: d, Agent: agent, Provider: "p",
		Grade: &Grade{Solved: solved, Steps: 10, USDMicros: 100},
	}
}

// cbug builds n attempts of agent on bug of which solved succeed.
func cbug(agent, bug string, n, solved int) []Result {
	var out []Result
	for i := 0; i < n; i++ {
		out = append(out, cres(agent, bug, Easy, i < solved))
	}
	return out
}

func rep(results ...[]Result) Report {
	var all []Result
	for _, r := range results {
		all = append(all, r...)
	}
	return Report{Suite: "s", Results: all}
}

func TestAgentName(t *testing.T) {
	for in, want := range map[string]string{"coder@2": "coder", "a@b@3": "a@b", "plain": "plain"} {
		if got := AgentName(in); got != want {
			t.Errorf("AgentName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompareMatchesByName(t *testing.T) {
	base := rep(cbug("coder@1", "x", 1, 1), cbug("other@1", "x", 1, 1))
	c, err := Compare(base, rep(cbug("coder@2", "x", 1, 1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pairs) != 1 || c.Pairs[0].AgentPair != (AgentPair{"coder@1", "coder@2"}) {
		t.Errorf("pairs = %+v", c.Pairs)
	}
	if !reflect.DeepEqual(c.BaseOnlyAgents, []string{"other@1"}) || len(c.NewOnlyAgents) != 0 || c.NewOnlyAgents == nil {
		t.Errorf("only = %v / %v", c.BaseOnlyAgents, c.NewOnlyAgents)
	}
}

func TestCompareErrors(t *testing.T) {
	tests := []struct {
		name     string
		base, nw Report
		mapping  []AgentPair
		want     string
	}{
		{
			"duplicate name", rep(cbug("coder@1", "x", 1, 1), cbug("coder@2", "x", 1, 1)), rep(cbug("coder@3", "x", 1, 1)), nil,
			`evals: compare: base report has agents "coder@1" and "coder@2" with the same name; use --map`,
		},
		{
			"duplicate name new", rep(cbug("coder@3", "x", 1, 1)), rep(cbug("coder@1", "x", 1, 1), cbug("coder@2", "x", 1, 1)), nil,
			`evals: compare: new report has agents "coder@1" and "coder@2" with the same name; use --map`,
		},
		{
			"no common name", rep(cbug("a@1", "x", 1, 1)), rep(cbug("b@1", "x", 1, 1)), nil,
			`evals: compare: no agent name appears in both reports; use --map BASE_AGENT=NEW_AGENT`,
		},
		{
			"unknown base", rep(cbug("a@1", "x", 1, 1)), rep(cbug("b@1", "x", 1, 1)),
			[]AgentPair{{"x@1", "b@1"}},
			`evals: compare: --map: agent "x@1" is not in the base report`,
		},
		{
			"unknown new", rep(cbug("a@1", "x", 1, 1)), rep(cbug("b@1", "x", 1, 1)),
			[]AgentPair{{"a@1", "y@1"}},
			`evals: compare: --map: agent "y@1" is not in the new report`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compare(tt.base, tt.nw, tt.mapping)
			if err == nil || err.Error() != tt.want {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCompareMap(t *testing.T) {
	c, err := Compare(rep(cbug("a@1", "x", 1, 1)), rep(cbug("b@1", "x", 1, 1)), []AgentPair{{"a@1", "b@1"}})
	if err != nil || len(c.Pairs) != 1 || c.Pairs[0].AgentPair != (AgentPair{"a@1", "b@1"}) {
		t.Errorf("c = %+v, err %v", c, err)
	}
}

func TestCompareCommonBugsOnly(t *testing.T) {
	base := rep(cbug("a@1", "x", 1, 1), cbug("a@1", "y", 1, 1))
	nw := rep(cbug("a@2", "y", 1, 1), cbug("a@2", "z", 1, 1))
	c, err := Compare(base, nw, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Pairs[0]
	last := p.Rows[len(p.Rows)-1]
	if last.Difficulty != "all" || last.Bugs != 1 || last.Base.Graded != 1 {
		t.Errorf("last row = %+v", last)
	}
	if !reflect.DeepEqual(p.BaseOnlyBugs, []string{"x"}) || !reflect.DeepEqual(p.NewOnlyBugs, []string{"z"}) {
		t.Errorf("only bugs = %v / %v", p.BaseOnlyBugs, p.NewOnlyBugs)
	}
}

func TestCompareNoCommonBug(t *testing.T) {
	c, err := Compare(rep(cbug("a@1", "x", 1, 1)), rep(cbug("a@2", "y", 1, 1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := c.Pairs[0]; len(p.Rows) != 0 || len(p.Changes) != 0 || p.Changes == nil {
		t.Errorf("pair = %+v", p)
	}
	var buf bytes.Buffer
	PrintComparison(&buf, c)
	if !strings.Contains(buf.String(), "a@1 -> a@2  bugs 0\n") || !strings.Contains(buf.String(), "no bug has results in both reports\n") {
		t.Errorf("output:\n%s", buf.String())
	}
}

func TestCompareStatusChanges(t *testing.T) {
	base := rep(cbug("a@1", "x", 3, 3), cbug("a@1", "y", 3, 1), cbug("a@1", "w", 3, 2), cbug("a@1", "e", 1, 1))
	nw := rep(cbug("a@2", "x", 3, 1), cbug("a@2", "y", 3, 3), cbug("a@2", "w", 3, 3),
		[]Result{{BugID: "e", Difficulty: Easy, Agent: "a@2", Provider: "p", Error: "boom"}})
	c, err := Compare(base, nw, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ch := range c.Pairs[0].Changes {
		got = append(got, ch.BugID+" "+ch.BaseStatus+"->"+ch.NewStatus)
	}
	want := []string{"e solved->error", "x solved->unsolved", "y unsolved->solved"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changes = %v, want %v", got, want)
	}
}

func TestCompareKNeedsRepeatsOnBothSides(t *testing.T) {
	for _, tt := range []struct{ b, n, want int }{{1, 3, 0}, {3, 3, 3}, {2, 3, 2}} {
		c, err := Compare(rep(cbug("a@1", "x", tt.b, 1)), rep(cbug("a@2", "x", tt.n, 1)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Pairs[0].K; got != tt.want {
			t.Errorf("K(%d,%d) = %d, want %d", tt.b, tt.n, got, tt.want)
		}
	}
}

func TestPrintComparison(t *testing.T) {
	bk := func(solved int, p1, p3 float64, usd int64, steps float64) AgentBucket {
		return AgentBucket{Solved: solved, PassAtK: []float64{p1, 0, p3}, MedianUSDMicrosSolved: usd, MedianStepsSolved: steps}
	}
	c := Comparison{
		Base:           ReportInfo{Path: "evals/results/it0.json", Suite: "go-bugs", Results: 60, Repeat: 3},
		New:            ReportInfo{Path: "evals/results/it1.json", Suite: "go-bugs", Results: 60, Repeat: 3},
		BaseOnlyAgents: []string{"coder-openai@1"}, NewOnlyAgents: []string{"coder-openai-plan@1"},
		Pairs: []PairComparison{{
			AgentPair: AgentPair{"coder-anthropic@1", "coder-anthropic@2"}, K: 3,
			Rows: []CompareRow{
				{"easy", 3, bk(8, 0.889, 1, 210_000, 14), bk(9, 1, 1, 180_000, 12)},
				{"medium", 4, bk(6, 0.5, 0.75, 400_000, 25), bk(7, 0.583, 1, 440_000, 27)},
				{"hard", 3, bk(1, 0.111, 0.333, 950_000, 41), bk(0, 0, 0, 0, 0)},
				{"all", 10, bk(15, 0.5, 0.7, 330_000, 20), bk(16, 0.533, 0.7, 300_000, 18.5)},
			},
			Changes: []BugChange{
				{
					BugID: "importcsv-parentheses", BaseStatus: "solved", NewStatus: "unsolved",
					Base: BugAttempts{Graded: 3, Solved: 3}, New: BugAttempts{Graded: 3, Solved: 1},
				},
				{
					BugID: "ledger-balance-asof", BaseStatus: "unsolved", NewStatus: "solved",
					Base: BugAttempts{Graded: 3, Solved: 1}, New: BugAttempts{Graded: 3, Solved: 3},
				},
			},
		}},
	}
	want := `base: evals/results/it0.json  suite go-bugs  results 60  repeat 3
new:  evals/results/it1.json  suite go-bugs  results 60  repeat 3
only in base: coder-openai@1
only in new: coder-openai-plan@1

coder-anthropic@1 -> coder-anthropic@2  bugs 10
DIFFICULTY  BUGS  PASS@1                   PASS@3                   MEDIAN COST              MEDIAN STEPS
easy        3     0.889 -> 1.000 (+0.111)  1.000 -> 1.000 (+0.000)  $0.21 -> $0.18 (-$0.03)  14 -> 12 (-2)
medium      4     0.500 -> 0.583 (+0.083)  0.750 -> 1.000 (+0.250)  $0.40 -> $0.44 (+$0.04)  25 -> 27 (+2)
hard        3     0.111 -> 0.000 (-0.111)  0.333 -> 0.000 (-0.333)  $0.95 -> - (n/a)         41 -> - (n/a)
all         10    0.500 -> 0.533 (+0.033)  0.700 -> 0.700 (+0.000)  $0.33 -> $0.30 (-$0.03)  20 -> 18.5 (-1.5)
status changes:
  importcsv-parentheses  solved 3/3 -> unsolved 1/3
  ledger-balance-asof    unsolved 1/3 -> solved 3/3
`
	var buf bytes.Buffer
	PrintComparison(&buf, c)
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestPrintComparisonNoChangesNoRepeats(t *testing.T) {
	c, err := Compare(rep(cbug("a@1", "x", 1, 1)), rep(cbug("a@2", "x", 1, 1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	PrintComparison(&buf, c)
	out := buf.String()
	if strings.Contains(out, "PASS@3") || strings.Contains(out, "PASS@0") || !strings.HasSuffix(out, "status changes: none\n") {
		t.Errorf("output:\n%s", out)
	}
}

func TestBugStatus(t *testing.T) {
	tests := []struct {
		graded, solved int
		want           string
	}{{0, 0, "error"}, {3, 2, "solved"}, {2, 1, "unsolved"}, {3, 1, "unsolved"}, {1, 1, "solved"}}
	for _, tt := range tests {
		if got := BugStatus(BugAttempts{Attempts: 3, Graded: tt.graded, Solved: tt.solved}); got != tt.want {
			t.Errorf("BugStatus(graded %d, solved %d) = %s, want %s", tt.graded, tt.solved, got, tt.want)
		}
	}
}
