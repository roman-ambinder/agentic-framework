package agentconfig_test

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
)

// TestLoadRepoAgents loads every agent file under agents/ with fake values for
// the environment variables the files use (model names and prices).
func TestLoadRepoAgents(t *testing.T) {
	for _, p := range []string{"ANTHROPIC", "OPENAI"} {
		t.Setenv("ADLC_"+p+"_MODEL", "test-model")
		for _, f := range []string{"INPUT", "OUTPUT", "CACHE_READ", "CACHE_WRITE"} {
			t.Setenv("ADLC_"+p+"_PRICE_"+f, "1.00")
		}
	}
	n := 0
	err := filepath.WalkDir("../../agents", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return err
		}
		n++
		if _, err := agentconfig.Load(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("found %d agent files under agents/, want at least 2", n)
	}
}
