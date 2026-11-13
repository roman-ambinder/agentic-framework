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
