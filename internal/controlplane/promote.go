package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/evals"
)

// Gate is what an eval report must show for a promotion.
type Gate struct {
	MinPassAt1         float64 // 0..1
	MaxMedianUSDMicros int64   // < 0 = no limit
}

// Promotion is one recorded promotion.
type Promotion struct {
	Name            string
	Version         int
	Env             Env
	ReportPath      string
	PassAt1         float64
	Runs            int
	MedianUSDMicros int64
	PromotedAt      time.Time
}

func usd(m int64) string { return fmt.Sprintf("%d.%06d", m/1_000_000, m%1_000_000) }

// Promote makes name@version the current version of env if rep proves it
// meets g. reportPath is only recorded.
func (s *Store) Promote(ctx context.Context, name string, version int, env Env,
	rep evals.Report, reportPath string, g Gate,
) (Promotion, error) {
	if g.MinPassAt1 < 0 || g.MinPassAt1 > 1 {
		return Promotion{}, fmt.Errorf("controlplane: min pass@1 must be between 0 and 1, got %v", g.MinPassAt1)
	}
	v, err := s.Version(ctx, name, version)
	if err != nil {
		return Promotion{}, err
	}
	ps, err := gateSummary(v, rep, g)
	if err != nil {
		return Promotion{}, err
	}
	p := Promotion{
		Name: name, Version: version, Env: env, ReportPath: reportPath,
		PassAt1: ps.PassAt1, Runs: ps.Runs, MedianUSDMicros: ps.MedianUSDMicrosSolved, PromotedAt: s.now().UTC(),
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO promotions (name, version, env, report_path, pass_at_1, runs, median_usd_micros, promoted_unix_nano)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Version, string(p.Env), p.ReportPath, p.PassAt1, p.Runs, p.MedianUSDMicros, p.PromotedAt.UnixNano()); err != nil {
		return Promotion{}, fmt.Errorf("controlplane: record promotion of %s: %w", v.Ref(), err)
	}
	return p, nil
}

// gateSummary checks that rep proves v and meets g; it returns the summary of v's results.
func gateSummary(v AgentVersion, rep evals.Report, g Gate) (evals.ProviderSummary, error) {
	ref := v.Ref()
	var results []evals.Result
	for _, r := range rep.Results {
		if r.AgentDigest == v.Digest {
			results = append(results, r)
		}
	}
	if len(results) == 0 {
		return evals.ProviderSummary{}, fmt.Errorf("%w: no result in the report has agent_digest %s (%s)", ErrReportMismatch, v.Digest, ref)
	}
	if allErrors(results) {
		return evals.ProviderSummary{}, fmt.Errorf("%w: all %d results of %s in the report have errors", ErrPromotionRefused, len(results), ref)
	}
	ps := evals.Summarize(results).ByProvider[results[0].Provider]
	if ps.PassAt1 < g.MinPassAt1 {
		return ps, fmt.Errorf("%w: %s: pass@1 %.3f over %d runs is below the minimum %.3f",
			ErrPromotionRefused, ref, ps.PassAt1, ps.Runs, g.MinPassAt1)
	}
	if g.MaxMedianUSDMicros >= 0 && ps.MedianUSDMicrosSolved > g.MaxMedianUSDMicros {
		return ps, fmt.Errorf("%w: %s: median cost of solved runs $%s is above the maximum $%s",
			ErrPromotionRefused, ref, usd(ps.MedianUSDMicrosSolved), usd(g.MaxMedianUSDMicros))
	}
	return ps, nil
}

func allErrors(results []evals.Result) bool {
	for _, r := range results {
		if r.Error == "" {
			return false
		}
	}
	return true
}
