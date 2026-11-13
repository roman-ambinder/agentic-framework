package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// clockAt returns a clock that reports the given unix seconds, one per call.
func clockAt(secs ...int64) func() time.Time {
	i := 0
	return func() time.Time {
		t := time.Unix(secs[i], 0)
		if i < len(secs)-1 {
			i++
		}
		return t
	}
}

func TestKillRejectsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		scope  Scope
		target string
		reason string
		want   string
	}{
		{"unknown scope", "team", "x", "r", `controlplane: invalid kill: unknown scope "team" (want run, agent or all)`},
		{"all with target", ScopeAll, "x", "r", "controlplane: invalid kill: scope all takes no target"},
		{"bad run id", ScopeRun, "a b", "r", `controlplane: invalid kill: invalid run id "a b"`},
		{"empty run id", ScopeRun, "", "r", `controlplane: invalid kill: invalid run id ""`},
		{"bad agent", ScopeAgent, "Bad", "r", `controlplane: invalid kill: invalid agent "Bad" (want NAME or NAME@VERSION)`},
		{"agent version not number", ScopeAgent, "hello@x", "r", `controlplane: invalid kill: invalid agent "hello@x" (want NAME or NAME@VERSION)`},
		{"empty reason", ScopeAll, "", " \t", "controlplane: invalid kill: reason is empty"},
	}
	s := openTemp(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.Kill(context.Background(), tt.scope, tt.target, tt.reason)
			if !errors.Is(err, ErrInvalidKill) || err.Error() != tt.want {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestKillTwiceReplacesReason(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, WithClock(clockAt(1, 2)))
	for _, r := range []string{"first", "second"} {
		if err := s.Kill(ctx, ScopeRun, "r-1", r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Active(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("Active = %v, %v", got, err)
	}
	if got[0].Reason != "second" || !got[0].CreatedAt.Equal(time.Unix(2, 0)) {
		t.Fatalf("kill = %+v", got[0])
	}
}

func TestUnkillMissing(t *testing.T) {
	err := openTemp(t).Unkill(context.Background(), ScopeRun, "r-1")
	if !errors.Is(err, ErrKillNotFound) || err.Error() != "controlplane: no such kill: run r-1" {
		t.Fatalf("err = %v", err)
	}
}

func TestUnkillRemoves(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.Kill(ctx, ScopeAll, "", "x"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unkill(ctx, ScopeAll, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Active(ctx); err != nil || len(got) != 0 {
		t.Fatalf("Active = %v, %v", got, err)
	}
}

func TestActiveOrder(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, WithClock(clockAt(3, 1, 2)))
	for _, id := range []string{"c", "a", "b"} {
		if err := s.Kill(ctx, ScopeRun, id, "x"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Active(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("Active = %v, %v", got, err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].Target != want {
			t.Fatalf("order = %v", got)
		}
		if got[i].CreatedAt.Location() != time.UTC {
			t.Fatalf("CreatedAt not UTC: %v", got[i].CreatedAt.Location())
		}
	}
}

func TestKilledMatching(t *testing.T) {
	type pair struct{ run, agent string }
	pairs := []pair{{"r-1", "x@1"}, {"r-2", "hello@1"}, {"r-2", "hello@2"}, {"r-2", "hel@1"}, {"r-2", "hello-x@1"}}
	tests := []struct {
		name  string
		kill  []Kill
		want  [5]bool
		wantR string
	}{
		{"none", nil, [5]bool{}, ""},
		{"run", []Kill{{Scope: ScopeRun, Target: "r-1", Reason: "R"}}, [5]bool{true}, "R"},
		{"agent name", []Kill{{Scope: ScopeAgent, Target: "hello", Reason: "A"}}, [5]bool{false, true, true}, "A"},
		{"agent id", []Kill{{Scope: ScopeAgent, Target: "hello@1", Reason: "A"}}, [5]bool{false, true}, "A"},
		{"all", []Kill{{Scope: ScopeAll, Reason: "L"}}, [5]bool{true, true, true, true, true}, "L"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			for _, k := range tt.kill {
				if err := s.Kill(ctx, k.Scope, k.Target, k.Reason); err != nil {
					t.Fatal(err)
				}
			}
			for i, p := range pairs {
				killed, reason, err := s.Killed(ctx, p.run, p.agent)
				if err != nil || killed != tt.want[i] {
					t.Fatalf("Killed(%v) = %v, %v (err %v), want %v", p, killed, reason, err, tt.want[i])
				}
				if killed && reason != tt.wantR || !killed && reason != "" {
					t.Fatalf("Killed(%v) reason = %q, want %q", p, reason, tt.wantR)
				}
			}
		})
	}
}

func TestKilledPrefersRunReason(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, WithClock(clockAt(1, 2)))
	if err := s.Kill(ctx, ScopeAll, "", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Kill(ctx, ScopeRun, "r-1", "r"); err != nil {
		t.Fatal(err)
	}
	if _, reason, err := s.Killed(ctx, "r-1", "x@1"); err != nil || reason != "r" {
		t.Fatalf("reason = %q, err %v", reason, err)
	}
}
