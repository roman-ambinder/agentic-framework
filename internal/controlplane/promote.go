package controlplane

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/evals"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
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
	// ImageID is the one sandbox image all non-errored results ran on; ""
	// when no result says (old reports).
	ImageID string
	// ResultsWithoutImage counts non-errored results of the version with no ImageID.
	ResultsWithoutImage int
	// ResultsChecked counts all non-errored results of the version (every
	// provider); ResultsWithoutImage is a part of it.
	ResultsChecked int
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
	ps, ev, err := gateSummary(v, rep, g)
	if err != nil {
		return Promotion{}, err
	}
	p := Promotion{
		Name: name, Version: version, Env: env, ReportPath: reportPath,
		PassAt1: ps.PassAt1, Runs: ps.Runs, MedianUSDMicros: ps.MedianUSDMicrosSolved, PromotedAt: s.now().UTC(),
		ImageID: ev.id, ResultsWithoutImage: ev.without, ResultsChecked: ev.checked,
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO promotions (name, version, env, report_path, pass_at_1, runs, median_usd_micros, promoted_unix_nano, image_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Version, string(p.Env), p.ReportPath, p.PassAt1, p.Runs, p.MedianUSDMicros, p.PromotedAt.UnixNano(), p.ImageID); err != nil {
		return Promotion{}, fmt.Errorf("controlplane: record promotion of %s: %w", v.Ref(), err)
	}
	return p, nil
}

// gateSummary checks that rep proves v and meets g; it returns the summary of
// v's results, the one image they ran on ("" if none says) and how many
// non-errored results do not say.
func gateSummary(v AgentVersion, rep evals.Report, g Gate) (evals.ProviderSummary, imageEvidence, error) {
	ref := v.Ref()
	var results []evals.Result
	for _, r := range rep.Results {
		if r.AgentDigest == v.Digest {
			results = append(results, r)
		}
	}
	if len(results) == 0 {
		return evals.ProviderSummary{}, imageEvidence{}, fmt.Errorf("%w: no result in the report has agent_digest %s (%s)", ErrReportMismatch, v.Digest, ref)
	}
	if allErrors(results) {
		return evals.ProviderSummary{}, imageEvidence{}, fmt.Errorf("%w: all %d results of %s in the report have errors", ErrPromotionRefused, len(results), ref)
	}
	ev, err := singleImage(ref, results)
	if err != nil {
		return evals.ProviderSummary{}, imageEvidence{}, err
	}
	ps := evals.Summarize(results).ByProvider[results[0].Provider]
	if ps.PassAt1 < g.MinPassAt1 {
		return ps, ev, fmt.Errorf("%w: %s: pass@1 %.3f over %d runs is below the minimum %.3f",
			ErrPromotionRefused, ref, ps.PassAt1, ps.Runs, g.MinPassAt1)
	}
	if g.MaxMedianUSDMicros >= 0 && ps.MedianUSDMicrosSolved > g.MaxMedianUSDMicros {
		return ps, ev, fmt.Errorf("%w: %s: median cost of solved runs $%s is above the maximum $%s",
			ErrPromotionRefused, ref, usd(ps.MedianUSDMicrosSolved), usd(g.MaxMedianUSDMicros))
	}
	return ps, ev, nil
}

// imageEvidence is what the non-errored results say about their image.
type imageEvidence struct {
	id      string // the one image ID, "" if none says
	without int    // results with no image ID
	checked int    // all non-errored results
}

// singleImage returns the one image ID of the non-errored results (""
// if none has one) and how many of them have none; two or more distinct IDs
// are refused.
func singleImage(ref string, results []evals.Result) (imageEvidence, error) {
	var ids []string
	var ev imageEvidence
	for _, r := range results {
		if r.Error == "" {
			ev.checked++
		}
		switch {
		case r.Error != "":
		case r.ImageID == "":
			ev.without++
		case !slices.Contains(ids, r.ImageID):
			ids = append(ids, r.ImageID)
		}
	}
	if len(ids) >= 2 {
		slices.Sort(ids)
		short := make([]string, len(ids))
		for i, id := range ids {
			short[i] = sandbox.ShortImageID(id)
		}
		return imageEvidence{}, fmt.Errorf("%w: %s: the results used %d images (%s); promote from evidence on one image",
			ErrPromotionRefused, ref, len(ids), strings.Join(short, ", "))
	}
	if len(ids) == 1 {
		ev.id = ids[0]
	}
	return ev, nil
}

func allErrors(results []evals.Result) bool {
	for _, r := range results {
		if r.Error == "" {
			return false
		}
	}
	return true
}
