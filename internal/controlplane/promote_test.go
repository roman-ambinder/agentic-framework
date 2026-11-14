package controlplane

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/evals"
)

func solved(digest string, usd int64) evals.Result {
	return evals.Result{AgentDigest: digest, Provider: "fake", Difficulty: evals.Easy, Grade: &evals.Grade{Solved: true, USDMicros: usd}}
}

func failed(digest string) evals.Result {
	return evals.Result{AgentDigest: digest, Provider: "fake", Difficulty: evals.Easy, Grade: &evals.Grade{}}
}

func errored(digest string) evals.Result {
	return evals.Result{AgentDigest: digest, Provider: "fake", Difficulty: evals.Easy, Error: "boom"}
}

func report(rs ...evals.Result) evals.Report { return evals.Report{Results: rs} }

var noCostLimit = Gate{MinPassAt1: 0.5, MaxMedianUSDMicros: -1}

// promoteEnv is a store with hello@1 (and hello@2 on request) registered.
func promoteEnv(t *testing.T) (*Store, *agentconfig.Agent) {
	t.Helper()
	s := openTemp(t)
	a := loadAgentFile(t, agentYAML("hello", "a"), nil)
	mustRegister(t, s, a)
	return s, a
}

func TestPromoteMeetsGate(t *testing.T) {
	s, a := promoteEnv(t)
	ctx := context.Background()
	d := a.Digest()
	p, err := s.Promote(ctx, "hello", 1, EnvStaging, report(solved(d, 0), solved(d, 0), failed(d)), "r.json", noCostLimit)
	if err != nil {
		t.Fatal(err)
	}
	if p.PassAt1 != 0.667 || p.Runs != 3 || p.Env != EnvStaging || p.ReportPath != "r.json" {
		t.Errorf("Promotion = %+v", p)
	}
	v, err := s.Resolve(ctx, Ref{Name: "hello", Env: EnvStaging})
	if err != nil || v.Version != 1 {
		t.Fatalf("Resolve = %+v, %v", v, err)
	}
	v1, _ := s.Version(ctx, "hello", 1)
	if !slices.Equal(v1.Envs, []Env{EnvStaging}) {
		t.Errorf("Envs = %v", v1.Envs)
	}
}

func TestPromoteRefusedBelowPassAt1(t *testing.T) {
	s, a := promoteEnv(t)
	d := a.Digest()
	_, err := s.Promote(context.Background(), "hello", 1, EnvStaging, report(solved(d, 0), failed(d), failed(d)), "r", noCostLimit)
	want := "controlplane: promotion refused: hello@1: pass@1 0.333 over 3 runs is below the minimum 0.500"
	if !errors.Is(err, ErrPromotionRefused) || err.Error() != want {
		t.Fatalf("err = %v, want %s", err, want)
	}
	if _, err := s.Resolve(context.Background(), Ref{Name: "hello", Env: EnvStaging}); !errors.Is(err, ErrNotPromoted) {
		t.Errorf("Resolve err = %v, want ErrNotPromoted", err)
	}
}

func TestPromoteRefusedAboveMedianCost(t *testing.T) {
	s, a := promoteEnv(t)
	d := a.Digest()
	rep := report(solved(d, 100000), solved(d, 300000))
	_, err := s.Promote(context.Background(), "hello", 1, EnvStaging, rep, "r", Gate{MinPassAt1: 0.5, MaxMedianUSDMicros: 150000})
	want := "median cost of solved runs $0.200000 is above the maximum $0.150000"
	if !errors.Is(err, ErrPromotionRefused) || !contains(err.Error(), want) {
		t.Fatalf("err = %v, want %s", err, want)
	}
	if _, err := s.Promote(context.Background(), "hello", 1, EnvStaging, rep, "r", noCostLimit); err != nil {
		t.Errorf("no limit: %v", err)
	}
}

func TestPromoteRefusesReportOfOtherVersion(t *testing.T) {
	s, a := promoteEnv(t)
	v2 := mustRegister(t, s, loadAgentFile(t, agentYAML("hello", "b"), nil))
	_, err := s.Promote(context.Background(), "hello", 1, EnvStaging, report(solved(v2.Digest, 0)), "r", noCostLimit)
	if !errors.Is(err, ErrReportMismatch) || !contains(err.Error(), a.Digest()) || !contains(err.Error(), "(hello@1)") {
		t.Errorf("err = %v", err)
	}
}

func TestPromoteRefusesReportWithoutDigest(t *testing.T) {
	s, _ := promoteEnv(t)
	_, err := s.Promote(context.Background(), "hello", 1, EnvStaging, report(solved("", 0)), "r", noCostLimit)
	if !errors.Is(err, ErrReportMismatch) {
		t.Errorf("err = %v, want ErrReportMismatch", err)
	}
}

func TestPromoteIgnoresOtherAgents(t *testing.T) {
	s, a := promoteEnv(t)
	d := a.Digest()
	p, err := s.Promote(context.Background(), "hello", 1, EnvStaging,
		report(solved(d, 0), solved(d, 0), failed("sha256:x"), failed("sha256:x"), failed("sha256:x")), "r", noCostLimit)
	if err != nil || p.PassAt1 != 1.0 {
		t.Errorf("Promote = %+v, %v", p, err)
	}
}

func TestPromoteAllErrors(t *testing.T) {
	s, a := promoteEnv(t)
	d := a.Digest()
	_, err := s.Promote(context.Background(), "hello", 1, EnvStaging, report(errored(d), errored(d)), "r", noCostLimit)
	if !errors.Is(err, ErrPromotionRefused) || !contains(err.Error(), "all 2 results of hello@1 in the report have errors") {
		t.Errorf("err = %v", err)
	}
}

func TestPromoteInvalidMin(t *testing.T) {
	s, a := promoteEnv(t)
	_, err := s.Promote(context.Background(), "hello", 1, EnvStaging, report(solved(a.Digest(), 0)), "r", Gate{MinPassAt1: 1.5})
	if err == nil || err.Error() != "controlplane: min pass@1 must be between 0 and 1, got 1.5" {
		t.Errorf("err = %v", err)
	}
}

func TestPromoteLatestWins(t *testing.T) {
	s, a := promoteEnv(t)
	ctx := context.Background()
	v2a := loadAgentFile(t, agentYAML("hello", "b"), nil)
	mustRegister(t, s, v2a)
	for _, ag := range []*agentconfig.Agent{a, v2a} {
		n := 1
		if ag == v2a {
			n = 2
		}
		if _, err := s.Promote(ctx, "hello", n, EnvProd, report(solved(ag.Digest(), 0)), "r", noCostLimit); err != nil {
			t.Fatal(err)
		}
	}
	v, err := s.Resolve(ctx, Ref{Name: "hello", Env: EnvProd})
	if err != nil || v.Version != 2 {
		t.Fatalf("Resolve = %+v, %v", v, err)
	}
	v1, _ := s.Version(ctx, "hello", 1)
	if len(v1.Envs) != 0 {
		t.Errorf("Version(1).Envs = %v, want empty", v1.Envs)
	}
}

func onImage(r evals.Result, id string) evals.Result {
	r.ImageID = id
	return r
}

const (
	imgA = "sha256:aaaaaaaaaaaa1111111111111111111111111111111111111111111111111111"
	imgB = "sha256:bbbbbbbbbbbb2222222222222222222222222222222222222222222222222222"
)

func TestPromoteImageRule(t *testing.T) {
	tests := []struct {
		name        string
		results     func(d string) []evals.Result
		wantErr     string
		wantImage   string
		wantNoImage int
	}{
		{"one image", func(d string) []evals.Result {
			return []evals.Result{onImage(solved(d, 0), imgA), onImage(solved(d, 0), imgA)}
		}, "", imgA, 0},
		{"two images", func(d string) []evals.Result {
			return []evals.Result{onImage(solved(d, 0), imgA), onImage(solved(d, 0), imgB)}
		}, "used 2 images (sha256:aaaaaaaaaaaa, sha256:bbbbbbbbbbbb)", "", 0},
		{"errored result without image is ignored", func(d string) []evals.Result {
			return []evals.Result{onImage(solved(d, 0), imgA), errored(d), onImage(errored(d), imgB)}
		}, "", imgA, 0},
		{"some results without image", func(d string) []evals.Result {
			return []evals.Result{onImage(solved(d, 0), imgA), solved(d, 0)}
		}, "", imgA, 1},
		{"old report without ids", func(d string) []evals.Result {
			return []evals.Result{solved(d, 0), solved(d, 0)}
		}, "", "", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, a := promoteEnv(t)
			p, err := s.Promote(context.Background(), "hello", 1, EnvStaging, report(tt.results(a.Digest())...), "r.json", noCostLimit)
			if tt.wantErr != "" {
				if !errors.Is(err, ErrPromotionRefused) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want refusal containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.ImageID != tt.wantImage || p.ResultsWithoutImage != tt.wantNoImage {
				t.Errorf("ImageID %q, ResultsWithoutImage %d; want %q, %d", p.ImageID, p.ResultsWithoutImage, tt.wantImage, tt.wantNoImage)
			}
		})
	}
}

func TestPromoteRecordsImageID(t *testing.T) {
	s, a := promoteEnv(t)
	d := a.Digest()
	if _, err := s.Promote(context.Background(), "hello", 1, EnvStaging, report(onImage(solved(d, 0), imgA)), "r.json", noCostLimit); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := s.db.QueryRow(`SELECT image_id FROM promotions WHERE name = 'hello'`).Scan(&got); err != nil || got != imgA {
		t.Fatalf("image_id = %q, %v; want %q", got, err, imgA)
	}
}
