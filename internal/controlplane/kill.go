package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Scope says what a kill applies to.
type Scope string

const (
	// ScopeRun kills one run; Target is a run id.
	ScopeRun Scope = "run"
	// ScopeAgent kills runs of an agent; Target is an agent name ("hello") or agent id ("hello@1").
	ScopeAgent Scope = "agent"
	// ScopeAll kills every run; Target is "".
	ScopeAll Scope = "all"
)

// Kill is one active kill.
type Kill struct {
	Scope     Scope
	Target    string
	Reason    string
	CreatedAt time.Time // UTC
}

var (
	// ErrInvalidKill means Kill or Unkill got a bad scope, target or reason.
	ErrInvalidKill = errors.New("controlplane: invalid kill")
	// ErrKillNotFound means Unkill found no such kill.
	ErrKillNotFound = errors.New("controlplane: no such kill")
)

var (
	runIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	agentRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}(@[0-9]+)?$`)
)

// describe returns "all" for ScopeAll, else "<scope> <target>" (for example "run r-1").
func describe(scope Scope, target string) string {
	if scope == ScopeAll {
		return "all"
	}
	return string(scope) + " " + target
}

func validateKill(scope Scope, target string) error {
	switch scope {
	case ScopeAll:
		if target != "" {
			return fmt.Errorf("%w: scope all takes no target", ErrInvalidKill)
		}
	case ScopeRun:
		if !runIDRE.MatchString(target) {
			return fmt.Errorf("%w: invalid run id %q", ErrInvalidKill, target)
		}
	case ScopeAgent:
		if !agentRE.MatchString(target) {
			return fmt.Errorf("%w: invalid agent %q (want NAME or NAME@VERSION)", ErrInvalidKill, target)
		}
	default:
		return fmt.Errorf("%w: unknown scope %q (want run, agent or all)", ErrInvalidKill, string(scope))
	}
	return nil
}

// Kill records a kill. Killing the same scope and target again replaces its
// reason and created time.
func (s *Store) Kill(ctx context.Context, scope Scope, target, reason string) error {
	if err := validateKill(scope, target); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: reason is empty", ErrInvalidKill)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO kills (scope, target, reason, created_unix_nano) VALUES (?, ?, ?, ?)
ON CONFLICT (scope, target) DO UPDATE SET reason = excluded.reason, created_unix_nano = excluded.created_unix_nano`,
		string(scope), target, reason, s.now().UnixNano())
	if err != nil {
		return fmt.Errorf("controlplane: kill %s: %w", describe(scope, target), err)
	}
	return nil
}

// Unkill removes a kill; ErrKillNotFound if there is none.
func (s *Store) Unkill(ctx context.Context, scope Scope, target string) error {
	if err := validateKill(scope, target); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM kills WHERE scope = ? AND target = ?`, string(scope), target)
	if err != nil {
		return fmt.Errorf("controlplane: unkill %s: %w", describe(scope, target), err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("controlplane: unkill %s: %w", describe(scope, target), err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrKillNotFound, describe(scope, target))
	}
	return nil
}

// Active returns every kill, oldest first (ties: scope, then target).
func (s *Store) Active(ctx context.Context) ([]Kill, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scope, target, reason, created_unix_nano FROM kills ORDER BY created_unix_nano, scope, target`)
	if err != nil {
		return nil, fmt.Errorf("controlplane: list kills: %w", err)
	}
	defer rows.Close()
	var out []Kill
	for rows.Next() {
		var k Kill
		var scope string
		var nano int64
		if err := rows.Scan(&scope, &k.Target, &k.Reason, &nano); err != nil {
			return nil, fmt.Errorf("controlplane: list kills: %w", err)
		}
		k.Scope, k.CreatedAt = Scope(scope), time.Unix(0, nano).UTC()
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("controlplane: list kills: %w", err)
	}
	return out, nil
}

// Killed reports whether a run must stop: it implements harness.KillSwitch.
// An agent kill matches agentID exactly ("hello@1") or by name ("hello").
func (s *Store) Killed(ctx context.Context, runID, agentID string) (bool, string, error) {
	agentName, _, _ := strings.Cut(agentID, "@")
	var reason string
	err := s.db.QueryRowContext(ctx, `SELECT reason FROM kills
WHERE scope = 'all'
   OR (scope = 'run'   AND target = ?)
   OR (scope = 'agent' AND (target = ? OR target = ?))
ORDER BY CASE scope WHEN 'run' THEN 1 WHEN 'agent' THEN 2 ELSE 3 END, created_unix_nano
LIMIT 1`, runID, agentID, agentName).Scan(&reason)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, "", nil
	case err != nil:
		return false, "", fmt.Errorf("controlplane: kill check: %w", err)
	}
	return true, reason, nil
}
