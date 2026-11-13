package mcprouter

import (
	"strings"
	"testing"
)

func TestServerConfigValidate(t *testing.T) {
	cmd := []string{"prog"}
	tests := []struct {
		name    string
		cfg     ServerConfig
		wantErr string
	}{
		{"ok", ServerConfig{Name: "fs", Command: cmd}, ""},
		{"empty name", ServerConfig{Name: "", Command: cmd}, "must match"},
		{"upper case", ServerConfig{Name: "FS", Command: cmd}, "must match"},
		{"double underscore", ServerConfig{Name: "my__fs", Command: cmd}, "must match"},
		{"dash", ServerConfig{Name: "a-b", Command: cmd}, "must match"},
		{"17 chars", ServerConfig{Name: strings.Repeat("a", 17), Command: cmd}, "must match"},
		{"16 chars", ServerConfig{Name: strings.Repeat("a", 16), Command: cmd}, ""},
		{"no command", ServerConfig{Name: "fs"}, `server "fs" has no command`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}
