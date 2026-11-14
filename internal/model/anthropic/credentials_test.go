package anthropic

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasCredentials(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		token   string
		profile bool
		want    bool
	}{
		{name: "nothing", want: false},
		{name: "api key", key: "k", want: true},
		{name: "auth token", token: "t", want: true},
		{name: "signed-in profile", profile: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("ANTHROPIC_CONFIG_DIR", dir)
			t.Setenv("ANTHROPIC_PROFILE", "default")
			t.Setenv("ANTHROPIC_API_KEY", tt.key)
			t.Setenv("ANTHROPIC_AUTH_TOKEN", tt.token)
			if tt.profile {
				writeProfile(t, dir)
			}
			if got := HasCredentials(); got != tt.want {
				t.Fatalf("HasCredentials() = %v, want %v", got, tt.want)
			}
		})
	}
}

// writeProfile writes a minimal signed-in profile config, as `ant auth login` would.
func writeProfile(t *testing.T, dir string) {
	t.Helper()
	p := filepath.Join(dir, "configs", "default.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"authentication":{"type":"user_oauth"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}
