package sandbox

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSpecValidate(t *testing.T) {
	valid := Spec{RunID: "r1", Image: "busybox", CPUs: 1.5, MemoryBytes: 1 << 20, Env: map[string]string{"A": "b"}}
	tests := []struct {
		name   string
		spec   Spec
		reason string
	}{
		{"empty run id", Spec{Image: "x"}, "run id is empty"},
		{"empty image", Spec{RunID: "r"}, "image is empty"},
		{"negative cpus", Spec{RunID: "r", Image: "x", CPUs: -1}, "cpus must not be negative"},
		{"negative memory", Spec{RunID: "r", Image: "x", MemoryBytes: -1}, "memory must not be negative"},
		{"run id checked first", Spec{CPUs: -1}, "run id is empty"},
		{"valid", valid, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if tt.reason == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("Validate() = %v, want ErrInvalidSpec", err)
			}
			if want := "sandbox: invalid spec: " + tt.reason; err.Error() != want {
				t.Fatalf("Validate() = %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestCapOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under", "abc", 5, "abc"},
		{"exact", "abcde", 5, "abcde"},
		{"over", "abcdefg", 5, "abcde" + TruncatedMarker},
		{"empty", "", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CapOutput([]byte(tt.in), tt.max)
			if !bytes.Equal(got, []byte(tt.want)) {
				t.Fatalf("CapOutput = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExpandArgs(t *testing.T) {
	in := []string{"--sandbox", "{{sandbox_id}}", "x={{sandbox_id}}", "{{sandbox_id}}:{{sandbox_id}}"}
	got := ExpandArgs(in, Handle{ID: "c1"})
	want := []string{"--sandbox", "c1", "x=c1", "c1:c1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandArgs() = %q, want %q", got, want)
	}
	if in[1] != "{{sandbox_id}}" || in[2] != "x={{sandbox_id}}" {
		t.Fatalf("input slice was modified: %q", in)
	}
}

func TestShortImageID(t *testing.T) {
	hex64 := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name, in, want string
	}{
		{"full sha256 id", "sha256:" + hex64, "sha256:0123456789ab"},
		{"already short", "sha256:0123456789ab", "sha256:0123456789ab"},
		{"wrong length", "sha256:" + hex64 + "00", "sha256:" + hex64 + "00"},
		{"non hex", "sha256:" + strings.Repeat("z", 64), "sha256:" + strings.Repeat("z", 64)},
		{"other prefix", "sha512:" + hex64, "sha512:" + hex64},
		{"plain ref", "golang:1.26", "golang:1.26"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShortImageID(tt.in); got != tt.want {
				t.Fatalf("ShortImageID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
