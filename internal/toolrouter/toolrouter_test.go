package toolrouter

import (
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

func TestNamespaced(t *testing.T) {
	if got := Namespaced("fs", "read_file"); got != "fs__read_file" {
		t.Fatalf("Namespaced = %q, want fs__read_file", got)
	}
}

func TestValidateTool(t *testing.T) {
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	valid := Tool{
		Spec:   model.ToolSpec{Name: "fs__read_file", InputSchema: []byte(`{"type":"object"}`)},
		Server: "fs",
		Risk:   adlcv1.RiskClass_RISK_CLASS_READ,
	}
	tests := []struct {
		name    string
		mutate  func(t *Tool)
		wantErr string
	}{
		{name: "valid tool passes", mutate: func(*Tool) {}},
		{
			name: "dot in name fails", mutate: func(tl *Tool) { tl.Spec.Name = "fs.read" },
			wantErr: `toolrouter: tool name "fs.read" does not match ^[a-zA-Z0-9_-]{1,64}$`,
		},
		{
			name: "65 character name fails", mutate: func(tl *Tool) { tl.Spec.Name = string(long) },
			wantErr: `does not match`,
		},
		{
			name: "unspecified risk fails", mutate: func(tl *Tool) { tl.Risk = adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED },
			wantErr: `toolrouter: tool "fs__read_file" has no risk class`,
		},
		{
			name: "empty server fails", mutate: func(tl *Tool) { tl.Server = "" },
			wantErr: `toolrouter: tool "fs__read_file" has no server`,
		},
		{
			name: "array schema fails", mutate: func(tl *Tool) { tl.Spec.InputSchema = []byte(`[1]`) },
			wantErr: `toolrouter: tool "fs__read_file" input schema is not a JSON object`,
		},
		{
			name: "null schema fails", mutate: func(tl *Tool) { tl.Spec.InputSchema = []byte(`null`) },
			wantErr: `input schema is not a JSON object`,
		},
		{name: "empty schema passes", mutate: func(tl *Tool) { tl.Spec.InputSchema = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tl := valid
			tt.mutate(&tl)
			err := ValidateTool(tl)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateTool error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateTool error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateTool error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}
