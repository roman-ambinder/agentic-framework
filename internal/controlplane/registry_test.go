package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
)

func agentYAML(name, system string) string {
	return "name: " + name + "\nversion: 1\nmodel:\n  provider: fake\nsystem: " + system + "\npolicy:\n  default: deny\n"
}

// loadAgentFile writes content to a new directory and loads it.
func loadAgentFile(t *testing.T, content string, extra map[string]string) *agentconfig.Agent {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for n, c := range extra {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, err := agentconfig.Load(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustRegister(t *testing.T, s *Store, a *agentconfig.Agent) AgentVersion {
	t.Helper()
	v, _, err := s.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRegisterAssignsVersions(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"first", agentYAML("hello", "a"), "hello@1"},
		{"changed content", agentYAML("hello", "b"), "hello@2"},
		{"other name", agentYAML("other", "a"), "other@1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, created, err := s.Register(ctx, loadAgentFile(t, tt.content, nil))
			if err != nil || !created || v.Ref() != tt.want {
				t.Fatalf("Register = %s, %v, %v; want %s created", v.Ref(), created, err, tt.want)
			}
		})
	}
}

func TestRegisterSameContentReturnsExisting(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	a := loadAgentFile(t, agentYAML("hello", "a"), nil)
	first := mustRegister(t, s, a)
	v, created, err := s.Register(ctx, a)
	if err != nil || created || v.Version != 1 {
		t.Fatalf("second Register = %+v, %v, %v", v, created, err)
	}
	b := loadAgentFile(t, agentYAML("hello", "a"), nil)
	v, created, err = s.Register(ctx, b)
	if err != nil || created || v.Version != 1 || v.SourceDir != first.SourceDir || v.SourceDir == b.Dir {
		t.Fatalf("copy Register = %+v, %v, %v (first dir %s)", v, created, err, first.SourceDir)
	}
}

func TestRegisterStoresFiles(t *testing.T) {
	s := openTemp(t)
	a := loadAgentFile(t, "name: hello\nversion: 1\nmodel:\n  provider: fake\nsystem_file: p.md\npolicy:\n  default: deny\n",
		map[string]string{"p.md": "hi\n"})
	mustRegister(t, s, a)
	v, err := s.Version(context.Background(), "hello", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Files, a.Sources) || v.Digest != a.Digest() || v.AgentID != "hello@1" {
		t.Errorf("Version = %+v; want files %v digest %s", v, a.Sources, a.Digest())
	}
}

func TestRegisterRejectsNonLocalFiles(t *testing.T) {
	s := openTemp(t)
	for _, p := range []string{"/etc/p.md", "../p.md"} {
		t.Run(p, func(t *testing.T) {
			a := loadAgentFile(t, agentYAML("hello", "a"), nil)
			a.Sources = append(a.Sources, agentconfig.SourceFile{Path: p, Data: []byte("x")})
			_, _, err := s.Register(context.Background(), a)
			want := `controlplane: agent cannot be registered: file "` + p + `" must be a relative path inside the agent file's directory`
			if !errors.Is(err, ErrInvalidAgent) || err.Error() != want {
				t.Errorf("err = %v, want %s", err, want)
			}
		})
	}
}

func TestRegisterRejectsClashWithAgentFile(t *testing.T) {
	s := openTemp(t)
	a := loadAgentFile(t, agentYAML("hello", "a"), nil)
	a.Sources = append(a.Sources, agentconfig.SourceFile{Path: "agent.yaml", Data: []byte("x")})
	_, _, err := s.Register(context.Background(), a)
	if !errors.Is(err, ErrInvalidAgent) || !strings.Contains(err.Error(), `file "agent.yaml" clashes with the agent file`) {
		t.Errorf("err = %v", err)
	}
}

func TestRegisterRejectsParsedAgent(t *testing.T) {
	s := openTemp(t)
	a, err := agentconfig.Parse([]byte(agentYAML("hello", "a")))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Register(context.Background(), a)
	want := "controlplane: agent cannot be registered: hello@1 was not loaded from a file"
	if !errors.Is(err, ErrInvalidAgent) || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

func TestVersionNotFound(t *testing.T) {
	s := openTemp(t)
	_, err := s.Version(context.Background(), "hello", 3)
	if !errors.Is(err, ErrVersionNotFound) || err.Error() != "controlplane: no such agent version: hello@3" {
		t.Errorf("err = %v", err)
	}
}

func TestVersionsAreImmutable(t *testing.T) {
	s := openTemp(t)
	mustRegister(t, s, loadAgentFile(t, agentYAML("hello", "a"), nil))
	for _, q := range []string{
		`UPDATE agent_versions SET agent_id = 'x'`,
		`DELETE FROM agent_versions`,
		`UPDATE agent_files SET data = x'00'`,
		`DELETE FROM agent_files`,
	} {
		if _, err := s.db.Exec(q); err == nil || !strings.Contains(err.Error(), "agent versions are immutable") {
			t.Errorf("%s: err = %v", q, err)
		}
	}
}

func TestParseRef(t *testing.T) {
	valid := map[string]Ref{
		"hello@1":       {Name: "hello", Version: 1},
		"hello@staging": {Name: "hello", Env: EnvStaging},
		"hello@prod":    {Name: "hello", Env: EnvProd},
	}
	for in, want := range valid {
		t.Run(in, func(t *testing.T) {
			got, err := ParseRef(in)
			if err != nil || got != want || got.String() != in {
				t.Errorf("ParseRef = %+v, %v; want %+v", got, err, want)
			}
		})
	}
	for _, in := range []string{"hello", "hello@0", "Hello@1", "hello@dev", "@1"} {
		t.Run("invalid "+in, func(t *testing.T) {
			_, err := ParseRef(in)
			if !errors.Is(err, ErrInvalidRef) {
				t.Errorf("err = %v, want ErrInvalidRef", err)
			}
		})
	}
}

func TestResolveEnvNotPromoted(t *testing.T) {
	s := openTemp(t)
	_, err := s.Resolve(context.Background(), Ref{Name: "hello", Env: EnvProd})
	if !errors.Is(err, ErrNotPromoted) || err.Error() != "controlplane: no version promoted: hello@prod" {
		t.Errorf("err = %v", err)
	}
}

func TestMigrateFromV1(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kills VALUES ('all', '', 'stop', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 2 {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	if kills, err := s.Active(ctx); err != nil || len(kills) != 1 {
		t.Fatalf("Active = %v, %v", kills, err)
	}
}
