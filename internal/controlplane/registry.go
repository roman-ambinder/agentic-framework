package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
)

// Env is a promotion target.
type Env string

const (
	// EnvStaging is the staging environment.
	EnvStaging Env = "staging"
	// EnvProd is the production environment.
	EnvProd Env = "prod"
)

// Ref names a registered version: NAME@N (Version > 0) or NAME@ENV (Env set).
type Ref struct {
	Name    string
	Version int
	Env     Env
}

var (
	// ErrInvalidRef means a NAME@N or NAME@ENV reference is malformed.
	ErrInvalidRef = errors.New("controlplane: invalid agent reference")
	// ErrInvalidAgent means an agent cannot be registered.
	ErrInvalidAgent = errors.New("controlplane: agent cannot be registered")
	// ErrVersionNotFound means no such registered version exists.
	ErrVersionNotFound = errors.New("controlplane: no such agent version")
	// ErrNotPromoted means no version has been promoted to the environment.
	ErrNotPromoted = errors.New("controlplane: no version promoted")
	// ErrReportMismatch means an eval report holds no result of the version.
	ErrReportMismatch = errors.New("controlplane: report was not produced by this version")
	// ErrPromotionRefused means the report does not meet the gate.
	ErrPromotionRefused = errors.New("controlplane: promotion refused")
)

var refRE = regexp.MustCompile(`^([a-z][a-z0-9-]{0,31})@([1-9][0-9]{0,8}|staging|prod)$`)

// ParseRef parses ^([a-z][a-z0-9-]{0,31})@([1-9][0-9]{0,8}|staging|prod)$.
func ParseRef(s string) (Ref, error) {
	m := refRE.FindStringSubmatch(s)
	if m == nil {
		return Ref{}, fmt.Errorf("%w: %q (want NAME@N, NAME@staging or NAME@prod)", ErrInvalidRef, s)
	}
	if n, err := strconv.Atoi(m[2]); err == nil {
		return Ref{Name: m[1], Version: n}, nil
	}
	return Ref{Name: m[1], Env: Env(m[2])}, nil
}

// String returns "name@N" or "name@env".
func (r Ref) String() string {
	if r.Env != "" {
		return r.Name + "@" + string(r.Env)
	}
	return r.Name + "@" + strconv.Itoa(r.Version)
}

// ParseEnv accepts "staging" and "prod".
func ParseEnv(s string) (Env, error) {
	switch Env(s) {
	case EnvStaging, EnvProd:
		return Env(s), nil
	}
	return "", fmt.Errorf("controlplane: unknown environment %q (want staging or prod)", s)
}

// AgentVersion is one registered version.
type AgentVersion struct {
	Name      string
	Version   int
	AgentID   string // the file's name@version: Config.AgentID of its runs
	Digest    string
	SourceDir string    // absolute directory of the file it was first registered from
	CreatedAt time.Time // UTC
	Envs      []Env     // environments whose current version this is; staging before prod
	// Files are in DigestFiles order; nil from Versions.
	Files []agentconfig.SourceFile
}

// Ref returns "name@N".
func (v AgentVersion) Ref() string { return v.Name + "@" + strconv.Itoa(v.Version) }

func checkRegistrable(a *agentconfig.Agent) error {
	if a.Dir == "" {
		return fmt.Errorf("%w: %s was not loaded from a file", ErrInvalidAgent, a.AgentID())
	}
	if len(a.Sources) == 0 {
		return fmt.Errorf("%w: %s has no source files", ErrInvalidAgent, a.AgentID())
	}
	for _, f := range a.Sources[1:] {
		switch {
		case f.Path == agentconfig.AgentFileName:
			return fmt.Errorf("%w: file %q clashes with the agent file", ErrInvalidAgent, f.Path)
		case !filepath.IsLocal(filepath.FromSlash(f.Path)):
			return fmt.Errorf("%w: file %q must be a relative path inside the agent file's directory", ErrInvalidAgent, f.Path)
		}
	}
	return nil
}

// Register stores a's Sources as a new version, or returns the version that
// already has a.Digest(); created reports which.
func (s *Store) Register(ctx context.Context, a *agentconfig.Agent) (v AgentVersion, created bool, err error) {
	if err := checkRegistrable(a); err != nil {
		return AgentVersion{}, false, err
	}
	d := a.Digest()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentVersion{}, false, fmt.Errorf("controlplane: register %s: %w", a.AgentID(), err)
	}
	defer func() { _ = tx.Rollback() }()
	var name string
	var version int
	err = tx.QueryRowContext(ctx, `SELECT name, version FROM agent_versions WHERE digest = ?`, d).Scan(&name, &version)
	if err == nil {
		_ = tx.Rollback()
		v, err = s.Version(ctx, name, version)
		return v, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentVersion{}, false, fmt.Errorf("controlplane: register %s: %w", a.AgentID(), err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM agent_versions WHERE name = ?`, a.Name).Scan(&n); err != nil {
		return AgentVersion{}, false, fmt.Errorf("controlplane: register %s: %w", a.AgentID(), err)
	}
	if err := insertVersion(ctx, tx, a, n+1, d, s.now()); err != nil {
		return AgentVersion{}, false, fmt.Errorf("controlplane: register %s: %w", a.AgentID(), err)
	}
	if err := tx.Commit(); err != nil {
		return AgentVersion{}, false, fmt.Errorf("controlplane: register %s: %w", a.AgentID(), err)
	}
	v, err = s.Version(ctx, a.Name, n+1)
	return v, true, err
}

func insertVersion(ctx context.Context, tx *sql.Tx, a *agentconfig.Agent, version int, digest string, now time.Time) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_versions (name, version, agent_id, digest, source_dir, created_unix_nano) VALUES (?, ?, ?, ?, ?, ?)`,
		a.Name, version, a.AgentID(), digest, a.Dir, now.UnixNano()); err != nil {
		return err
	}
	for _, f := range a.Sources {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agent_files (name, version, path, data) VALUES (?, ?, ?, ?)`,
			a.Name, version, f.Path, f.Data); err != nil {
			return err
		}
	}
	return nil
}

// Version returns name@version with its files.
func (s *Store) Version(ctx context.Context, name string, version int) (AgentVersion, error) {
	v, err := s.versionRow(ctx, name, version)
	if err != nil {
		return AgentVersion{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path, data FROM agent_files WHERE name = ? AND version = ? ORDER BY path`, name, version)
	if err != nil {
		return AgentVersion{}, fmt.Errorf("controlplane: read files of %s: %w", v.Ref(), err)
	}
	defer rows.Close()
	for rows.Next() {
		var f agentconfig.SourceFile
		if err := rows.Scan(&f.Path, &f.Data); err != nil {
			return AgentVersion{}, fmt.Errorf("controlplane: read files of %s: %w", v.Ref(), err)
		}
		v.Files = append(v.Files, f)
	}
	if err := rows.Err(); err != nil {
		return AgentVersion{}, fmt.Errorf("controlplane: read files of %s: %w", v.Ref(), err)
	}
	// agent.yaml first, the rest ordered by path (as DigestFiles).
	slices.SortStableFunc(v.Files, func(a, b agentconfig.SourceFile) int {
		return fileRank(a) - fileRank(b)
	})
	return v, nil
}

func fileRank(f agentconfig.SourceFile) int {
	if f.Path == agentconfig.AgentFileName {
		return 0
	}
	return 1
}

// versionRow reads one version without files, with its Envs.
func (s *Store) versionRow(ctx context.Context, name string, version int) (AgentVersion, error) {
	v := AgentVersion{Name: name, Version: version}
	var nano int64
	err := s.db.QueryRowContext(ctx,
		`SELECT agent_id, digest, source_dir, created_unix_nano FROM agent_versions WHERE name = ? AND version = ?`,
		name, version).Scan(&v.AgentID, &v.Digest, &v.SourceDir, &nano)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentVersion{}, fmt.Errorf("%w: %s@%d", ErrVersionNotFound, name, version)
	}
	if err != nil {
		return AgentVersion{}, fmt.Errorf("controlplane: read %s@%d: %w", name, version, err)
	}
	v.CreatedAt = time.Unix(0, nano).UTC()
	for _, env := range []Env{EnvStaging, EnvProd} {
		cur, err := s.current(ctx, name, env)
		if err != nil {
			return AgentVersion{}, err
		}
		if cur == version {
			v.Envs = append(v.Envs, env)
		}
	}
	return v, nil
}

// current returns the current version of env, or 0 if none was promoted.
func (s *Store) current(ctx context.Context, name string, env Env) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT version FROM promotions WHERE name = ? AND env = ? ORDER BY id DESC LIMIT 1`, name, string(env)).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("controlplane: read %s@%s: %w", name, env, err)
	}
	return n, nil
}

// Versions returns every version without files, sorted by name, then version.
func (s *Store) Versions(ctx context.Context) ([]AgentVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, version FROM agent_versions ORDER BY name, version`)
	if err != nil {
		return nil, fmt.Errorf("controlplane: list versions: %w", err)
	}
	type key struct {
		name    string
		version int
	}
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.name, &k.version); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("controlplane: list versions: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("controlplane: list versions: %w", err)
	}
	_ = rows.Close()
	var out []AgentVersion
	for _, k := range keys {
		v, err := s.versionRow(ctx, k.name, k.version)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Resolve returns the version ref names (NAME@N, or the current version of NAME@ENV), with files.
func (s *Store) Resolve(ctx context.Context, ref Ref) (AgentVersion, error) {
	if ref.Env == "" {
		return s.Version(ctx, ref.Name, ref.Version)
	}
	n, err := s.current(ctx, ref.Name, ref.Env)
	if err != nil {
		return AgentVersion{}, err
	}
	if n == 0 {
		return AgentVersion{}, fmt.Errorf("%w: %s@%s", ErrNotPromoted, ref.Name, ref.Env)
	}
	return s.Version(ctx, ref.Name, n)
}
