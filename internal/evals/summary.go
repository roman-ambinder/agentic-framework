package evals

import (
	"cmp"
	"math"
	"slices"
)

// Bucket counts results.
type Bucket struct {
	Runs    int     `json:"runs"`
	Solved  int     `json:"solved"`
	PassAt1 float64 `json:"pass_at_1"` // Solved / Runs, 0 when Runs == 0; rounded to 3 decimals
}

// ProviderSummary aggregates one provider's results.
type ProviderSummary struct {
	Bucket                                      // all difficulties
	ByDifficulty          map[Difficulty]Bucket `json:"by_difficulty"` // always has easy, medium, hard keys
	Errors                int                   `json:"errors"`        // results with Error != ""; NOT counted in Runs
	MedianUSDMicrosSolved int64                 `json:"median_usd_micros_solved"`
	MedianStepsSolved     float64               `json:"median_steps_solved"`
	TotalUSDMicros        int64                 `json:"total_usd_micros"` // all graded results
	StopReasons           map[string]int        `json:"stop_reasons"`     // stop reason name -> count, graded results
}

// Summary aggregates a report.
type Summary struct {
	ByProvider map[string]ProviderSummary `json:"by_provider"`
	ByAgent    map[string]AgentSummary    `json:"by_agent,omitempty"`
}

// Summarize computes the summary of results. Results with an error are
// counted only in Errors.
func Summarize(results []Result) Summary {
	s := Summary{ByProvider: map[string]ProviderSummary{}}
	solvedUSD := map[string][]int64{}
	solvedSteps := map[string][]int64{}
	for _, r := range results {
		ps, ok := s.ByProvider[r.Provider]
		if !ok {
			ps.ByDifficulty = map[Difficulty]Bucket{Easy: {}, Medium: {}, Hard: {}}
			ps.StopReasons = map[string]int{}
		}
		if r.Error != "" {
			ps.Errors++
			s.ByProvider[r.Provider] = ps
			continue
		}
		solved := r.Solved()
		ps.Bucket = count(ps.Bucket, solved)
		ps.ByDifficulty[r.Difficulty] = count(ps.ByDifficulty[r.Difficulty], solved)
		if r.StopReason != "" {
			ps.StopReasons[r.StopReason]++
		}
		if r.Grade != nil {
			ps.TotalUSDMicros += r.Grade.USDMicros
			if solved {
				solvedUSD[r.Provider] = append(solvedUSD[r.Provider], r.Grade.USDMicros)
				solvedSteps[r.Provider] = append(solvedSteps[r.Provider], int64(r.Grade.Steps))
			}
		}
		s.ByProvider[r.Provider] = ps
	}
	for p, ps := range s.ByProvider {
		ps.Bucket = finish(ps.Bucket)
		for d, b := range ps.ByDifficulty {
			ps.ByDifficulty[d] = finish(b)
		}
		ps.MedianUSDMicrosSolved = median(solvedUSD[p])
		ps.MedianStepsSolved = medianFloat(solvedSteps[p])
		s.ByProvider[p] = ps
	}
	s.ByAgent = SummarizeAgents(results)
	return s
}

func count(b Bucket, solved bool) Bucket {
	b.Runs++
	if solved {
		b.Solved++
	}
	return b
}

// finish computes PassAt1 from the counts.
func finish(b Bucket) Bucket {
	if b.Runs > 0 {
		b.PassAt1 = math.Round(float64(b.Solved)/float64(b.Runs)*1000) / 1000
	}
	return b
}

// median returns the middle value of xs; for an even count the integer mean
// of the two middle values, rounded down. Empty gives 0.
func median(xs []int64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.SortFunc(s, cmp.Compare[int64])
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	a, b := s[n/2-1], s[n/2]
	return a + (b-a)/2 // no overflow; equals floor((a+b)/2) for a <= b
}

// medianFloat is median with a fractional mean for an even count.
func medianFloat(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.SortFunc(s, cmp.Compare[int64])
	n := len(s)
	if n%2 == 1 {
		return float64(s[n/2])
	}
	return float64(s[n/2-1]+s[n/2]) / 2
}

// PassAtK is the unbiased pass@k estimate for n attempts of which c solved:
// 1 - C(n-c, k) / C(n, k). It returns 0 unless 1 <= k <= n and 0 <= c <= n.
func PassAtK(n, c, k int) float64 {
	if k < 1 || k > n || c < 0 || c > n {
		return 0
	}
	if n-c < k {
		return 1
	}
	p := 1.0
	for i := n - c + 1; i <= n; i++ {
		p *= 1 - float64(k)/float64(i)
	}
	return 1 - p
}

// BugAttempts counts one agent's attempts on one bug.
type BugAttempts struct {
	BugID      string     `json:"bug_id"`
	Difficulty Difficulty `json:"difficulty"`
	Attempts   int        `json:"attempts"` // results, including errors
	Graded     int        `json:"graded"`   // results with Error == ""
	Solved     int        `json:"solved"`   // results with Solved()
}

// AgentBucket aggregates one agent over a set of bugs.
type AgentBucket struct {
	BugCount              int       `json:"bug_count"` // bugs with Graded >= 1
	Graded                int       `json:"graded"`
	Solved                int       `json:"solved"`
	PassAtK               []float64 `json:"pass_at_k"` // [k-1] = pass@k, k = 1..MaxAttempts; 3 decimals
	MedianUSDMicrosSolved int64     `json:"median_usd_micros_solved"`
	MedianStepsSolved     float64   `json:"median_steps_solved"`
}

// AgentSummary aggregates one agent's results.
type AgentSummary struct {
	AgentBucket                             // all difficulties
	Provider     string                     `json:"provider"`
	MaxAttempts  int                        `json:"max_attempts"` // most Attempts of any bug
	Errors       int                        `json:"errors"`
	ByDifficulty map[Difficulty]AgentBucket `json:"by_difficulty"` // always easy, medium, hard
	PerBug       []BugAttempts              `json:"per_bug"`       // sorted by BugID
}

// agentAcc collects one agent's results while SummarizeAgents walks them.
type agentAcc struct {
	provider string
	errors   int
	bugs     map[string]*BugAttempts
	usd      map[Difficulty][]int64
	steps    map[Difficulty][]int64
}

// SummarizeAgents summarizes results per agent id (Result.Agent).
func SummarizeAgents(results []Result) map[string]AgentSummary {
	accs := map[string]*agentAcc{}
	for _, r := range results {
		a, ok := accs[r.Agent]
		if !ok {
			a = &agentAcc{
				provider: r.Provider, bugs: map[string]*BugAttempts{},
				usd: map[Difficulty][]int64{}, steps: map[Difficulty][]int64{},
			}
			accs[r.Agent] = a
		}
		a.add(r)
	}
	out := make(map[string]AgentSummary, len(accs))
	for id, a := range accs {
		out[id] = a.summary()
	}
	return out
}

func (a *agentAcc) add(r Result) {
	b, ok := a.bugs[r.BugID]
	if !ok {
		b = &BugAttempts{BugID: r.BugID, Difficulty: r.Difficulty}
		a.bugs[r.BugID] = b
	}
	b.Attempts++
	if r.Error == "" {
		b.Graded++
	} else {
		a.errors++
	}
	if r.Solved() {
		b.Solved++
	}
	if r.Solved() && r.Grade != nil {
		a.usd[""] = append(a.usd[""], r.Grade.USDMicros)
		a.steps[""] = append(a.steps[""], int64(r.Grade.Steps))
		a.usd[r.Difficulty] = append(a.usd[r.Difficulty], r.Grade.USDMicros)
		a.steps[r.Difficulty] = append(a.steps[r.Difficulty], int64(r.Grade.Steps))
	}
}

func (a *agentAcc) summary() AgentSummary {
	perBug := make([]BugAttempts, 0, len(a.bugs))
	maxK := 0
	for _, b := range a.bugs {
		perBug = append(perBug, *b)
		maxK = max(maxK, b.Attempts)
	}
	slices.SortFunc(perBug, func(x, y BugAttempts) int { return cmp.Compare(x.BugID, y.BugID) })
	s := AgentSummary{
		Provider: a.provider, MaxAttempts: maxK, Errors: a.errors, PerBug: perBug,
		ByDifficulty: map[Difficulty]AgentBucket{},
	}
	s.AgentBucket = bucketOf(perBug, a.usd[""], a.steps[""], maxK)
	for _, d := range []Difficulty{Easy, Medium, Hard} {
		var bugs []BugAttempts
		for _, b := range perBug {
			if b.Difficulty == d {
				bugs = append(bugs, b)
			}
		}
		s.ByDifficulty[d] = bucketOf(bugs, a.usd[d], a.steps[d], maxK)
	}
	return s
}

// bucketOf aggregates bugs into an AgentBucket with pass@k for k = 1..maxK.
func bucketOf(bugs []BugAttempts, usd, steps []int64, maxK int) AgentBucket {
	bk := AgentBucket{PassAtK: make([]float64, maxK)}
	for _, b := range bugs {
		if b.Graded >= 1 {
			bk.BugCount++
		}
		bk.Graded += b.Graded
		bk.Solved += b.Solved
	}
	for k := 1; k <= maxK; k++ {
		sum, n := 0.0, 0
		for _, b := range bugs {
			if b.Graded >= k {
				sum += PassAtK(b.Graded, b.Solved, k)
				n++
			}
		}
		if n > 0 {
			bk.PassAtK[k-1] = math.Round(sum/float64(n)*1000) / 1000
		}
	}
	bk.MedianUSDMicrosSolved = median(usd)
	bk.MedianStepsSolved = medianFloat(steps)
	return bk
}
