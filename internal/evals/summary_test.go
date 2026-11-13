package evals

import (
	"math"
	"reflect"
	"testing"
)

func graded(provider string, d Difficulty, solved bool, steps int, usd int64, stop string) Result {
	return Result{
		Provider: provider, Difficulty: d, StopReason: stop,
		Hidden: TestOutcome{Passed: []string{"T"}},
		Grade:  &Grade{Solved: solved, Steps: steps, USDMicros: usd},
	}
}

func TestSummarizeCounts(t *testing.T) {
	results := []Result{
		graded("anthropic", Easy, true, 1, 1, ""),
		graded("anthropic", Medium, false, 1, 1, ""),
		graded("anthropic", Medium, true, 1, 1, ""),
		graded("anthropic", Hard, true, 1, 1, ""),
		{Provider: "anthropic", Difficulty: Hard, Error: "boom"},
		graded("openai", Easy, false, 1, 1, ""),
		graded("openai", Hard, false, 1, 1, ""),
		graded("openai", Hard, false, 1, 1, ""),
	}
	s := Summarize(results)
	a, o := s.ByProvider["anthropic"], s.ByProvider["openai"]
	if want := (Bucket{Runs: 4, Solved: 3, PassAt1: 0.75}); a.Bucket != want {
		t.Errorf("anthropic bucket = %+v, want %+v", a.Bucket, want)
	}
	wantDiff := map[Difficulty]Bucket{Easy: {1, 1, 1}, Medium: {2, 1, 0.5}, Hard: {1, 1, 1}}
	if !reflect.DeepEqual(a.ByDifficulty, wantDiff) || a.Errors != 1 {
		t.Errorf("anthropic = %+v", a)
	}
	if want := (Bucket{Runs: 3, Solved: 0}); o.Bucket != want || o.Errors != 0 || o.ByDifficulty[Hard] != (Bucket{Runs: 2}) {
		t.Errorf("openai = %+v", o)
	}
	if third := Summarize([]Result{graded("p", Easy, true, 1, 1, ""), graded("p", Easy, false, 1, 1, ""), graded("p", Easy, false, 1, 1, "")}); third.ByProvider["p"].PassAt1 != 0.333 {
		t.Errorf("PassAt1 = %v, want 0.333", third.ByProvider["p"].PassAt1)
	}
}

func TestSummarizeMedians(t *testing.T) {
	tests := []struct {
		name      string
		results   []Result
		wantUSD   int64
		wantSteps float64
		wantTotal int64
	}{
		{"odd", []Result{
			graded("p", Easy, true, 30, 300, ""), graded("p", Easy, true, 10, 100, ""), graded("p", Easy, true, 20, 900, ""),
		}, 300, 20, 1300},
		{"even", []Result{
			graded("p", Easy, true, 10, 100, ""), graded("p", Easy, true, 21, 201, ""),
		}, 150, 15.5, 301},
		{"unsolved ignored for medians but not total", []Result{
			graded("p", Easy, true, 10, 100, ""), graded("p", Easy, false, 99, 5000, ""),
		}, 100, 10, 5100},
		{"none solved", []Result{graded("p", Easy, false, 5, 50, "")}, 0, 0, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Summarize(tt.results).ByProvider["p"]
			if p.MedianUSDMicrosSolved != tt.wantUSD || p.MedianStepsSolved != tt.wantSteps || p.TotalUSDMicros != tt.wantTotal {
				t.Errorf("got usd %d steps %v total %d", p.MedianUSDMicrosSolved, p.MedianStepsSolved, p.TotalUSDMicros)
			}
		})
	}
}

func TestSummarizeEmptyDifficultyKeysPresent(t *testing.T) {
	p := Summarize([]Result{graded("p", Easy, true, 1, 1, "")}).ByProvider["p"]
	for _, d := range []Difficulty{Easy, Medium, Hard} {
		if _, ok := p.ByDifficulty[d]; !ok {
			t.Errorf("key %q missing", d)
		}
	}
	if p.ByDifficulty[Hard] != (Bucket{}) {
		t.Errorf("hard = %+v", p.ByDifficulty[Hard])
	}
	// A provider with only errors still has the keys.
	e := Summarize([]Result{{Provider: "q", Error: "x"}}).ByProvider["q"]
	if len(e.ByDifficulty) != 3 || e.Errors != 1 || e.Runs != 0 {
		t.Errorf("error-only provider = %+v", e)
	}
}

func TestSummarizeStopReasons(t *testing.T) {
	s := Summarize([]Result{
		graded("p", Easy, true, 1, 1, "STOP_REASON_COMPLETED"),
		graded("p", Easy, false, 1, 1, "STOP_REASON_COMPLETED"),
		graded("p", Easy, false, 1, 1, "STOP_REASON_BUDGET_EXHAUSTED"),
		{Provider: "p", StopReason: "STOP_REASON_FAILED", Error: "boom"},
	})
	want := map[string]int{"STOP_REASON_COMPLETED": 2, "STOP_REASON_BUDGET_EXHAUSTED": 1}
	if got := s.ByProvider["p"].StopReasons; !reflect.DeepEqual(got, want) {
		t.Errorf("StopReasons = %v, want %v", got, want)
	}
}

func TestPassAtK(t *testing.T) {
	tests := []struct {
		n, c, k int
		want    float64
	}{
		{3, 0, 1, 0},
		{3, 3, 1, 1},
		{3, 1, 1, 1.0 / 3},
		{3, 1, 2, 2.0 / 3},
		{3, 1, 3, 1},
		{5, 2, 2, 0.7},
		{4, 0, 4, 0},
		{1, 1, 1, 1},
		{2, 1, 3, 0},
		{2, 1, 0, 0},
	}
	for _, tt := range tests {
		got := PassAtK(tt.n, tt.c, tt.k)
		if math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("PassAtK(%d,%d,%d) = %v, want %v", tt.n, tt.c, tt.k, got, tt.want)
		}
	}
}

// attempt builds a graded result for agent a on bug.
func attempt(bug string, d Difficulty, solved bool) Result {
	r := graded("p", d, solved, 1, 1, "")
	r.Agent, r.BugID = "a@1", bug
	return r
}

func TestSummarizeAgentsPassAtK(t *testing.T) {
	results := []Result{
		attempt("x", Easy, true), attempt("x", Easy, false), attempt("x", Easy, false),
		attempt("y", Medium, true), attempt("y", Medium, true), attempt("y", Medium, true),
	}
	a := SummarizeAgents(results)["a@1"]
	if a.MaxAttempts != 3 || !reflect.DeepEqual(a.PassAtK, []float64{0.667, 0.833, 1}) {
		t.Errorf("max %d pass %v", a.MaxAttempts, a.PassAtK)
	}
	if got := a.ByDifficulty[Easy].PassAtK; !reflect.DeepEqual(got, []float64{0.333, 0.667, 1}) {
		t.Errorf("easy pass = %v", got)
	}
	if got := a.ByDifficulty[Hard]; !reflect.DeepEqual(got, AgentBucket{PassAtK: []float64{0, 0, 0}}) {
		t.Errorf("hard = %+v", got)
	}
	if len(a.PerBug) != 2 || a.PerBug[0].BugID != "x" || a.PerBug[1].BugID != "y" {
		t.Errorf("per bug = %+v", a.PerBug)
	}
}

func TestSummarizeAgentsErrorsReduceGraded(t *testing.T) {
	results := []Result{
		attempt("z", Easy, true), attempt("z", Easy, false),
		{Agent: "a@1", BugID: "z", Difficulty: Easy, Provider: "p", Error: "boom"},
	}
	a := SummarizeAgents(results)["a@1"]
	if want := (BugAttempts{BugID: "z", Difficulty: Easy, Attempts: 3, Graded: 2, Solved: 1}); a.PerBug[0] != want {
		t.Errorf("per bug = %+v, want %+v", a.PerBug[0], want)
	}
	if a.Errors != 1 || !reflect.DeepEqual(a.PassAtK, []float64{0.5, 1, 0}) {
		t.Errorf("errors %d pass %v", a.Errors, a.PassAtK)
	}
}

func TestSummarizeAgentsOneAttemptPerBug(t *testing.T) {
	var results []Result
	for i, bug := range []string{"a", "b", "c", "d"} {
		results = append(results, attempt(bug, Easy, i < 2)) // Attempt is 0
	}
	a := SummarizeAgents(results)["a@1"]
	if a.MaxAttempts != 1 || !reflect.DeepEqual(a.PassAtK, []float64{0.5}) {
		t.Errorf("max %d pass %v", a.MaxAttempts, a.PassAtK)
	}
}

func TestSummarizeAgentsMedians(t *testing.T) {
	mk := func(bug string, d Difficulty, solved bool, usd int64) Result {
		r := graded("p", d, solved, int(usd/100), usd, "")
		r.Agent, r.BugID = "a@1", bug
		return r
	}
	results := []Result{
		mk("a", Easy, true, 100), mk("b", Easy, true, 300), mk("c", Hard, true, 900), mk("d", Hard, false, 5000),
	}
	a := SummarizeAgents(results)["a@1"]
	if a.MedianUSDMicrosSolved != 300 || a.ByDifficulty[Easy].MedianUSDMicrosSolved != 200 || a.ByDifficulty[Hard].MedianUSDMicrosSolved != 900 {
		t.Errorf("medians: all %d easy %d hard %d", a.MedianUSDMicrosSolved, a.ByDifficulty[Easy].MedianUSDMicrosSolved, a.ByDifficulty[Hard].MedianUSDMicrosSolved)
	}
}
