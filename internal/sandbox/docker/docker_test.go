package docker

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

func TestSanitize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"run/1 x", "run-1-x"},
		{"Abc_1.2-z", "Abc_1.2-z"},
		{"", ""},
		{"a:b@c", "a-b-c"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := sanitize(tt.in); got != tt.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEnvListSorted(t *testing.T) {
	got := envList(map[string]string{"B": "2", "A": "1", "C": "x=y"})
	want := []string{"A=1", "B=2", "C=x=y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("envList = %v, want %v", got, want)
	}
	if got := envList(nil); len(got) != 0 {
		t.Fatalf("envList(nil) = %v", got)
	}
}

func TestLimitedBuffer(t *testing.T) {
	const limit = 10
	b := &limitedBuffer{limit: limit}
	chunk := make([]byte, limit)
	total := 0
	for range 3 {
		n, err := b.Write(chunk)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != 3*limit {
		t.Fatalf("reported %d bytes written, want %d", total, 3*limit)
	}
	if len(b.Bytes()) != limit {
		t.Fatalf("kept %d bytes, want %d", len(b.Bytes()), limit)
	}
}

func TestNewUnavailable(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent.sock")
	_, err := New(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("New err = %v, want ErrUnavailable", err)
	}
}

func TestHostConfigNoNetworkByDefault(t *testing.T) {
	hc := hostConfig(sandbox.Spec{})
	if hc.NetworkMode != "none" {
		t.Fatalf("NetworkMode = %q, want none", hc.NetworkMode)
	}
}

func TestHostConfigNetworkWhenAsked(t *testing.T) {
	hc := hostConfig(sandbox.Spec{Network: true})
	if hc.NetworkMode != "" {
		t.Fatalf("NetworkMode = %q, want empty", hc.NetworkMode)
	}
}

func TestHostConfigLimits(t *testing.T) {
	hc := hostConfig(sandbox.Spec{CPUs: 1.5, MemoryBytes: 64 << 20})
	if hc.NanoCPUs != 1_500_000_000 {
		t.Fatalf("NanoCPUs = %d", hc.NanoCPUs)
	}
	if hc.Memory != 64<<20 || hc.MemorySwap != 64<<20 {
		t.Fatalf("Memory = %d, MemorySwap = %d", hc.Memory, hc.MemorySwap)
	}
	if hc.PidsLimit == nil || *hc.PidsLimit != 512 {
		t.Fatalf("PidsLimit = %v", hc.PidsLimit)
	}
	if hc.CPUQuota != 0 {
		t.Fatalf("CPUQuota = %d, must stay 0 when NanoCPUs is set", hc.CPUQuota)
	}
}

func TestHostConfigNoLimitsWhenZero(t *testing.T) {
	hc := hostConfig(sandbox.Spec{})
	if hc.NanoCPUs != 0 || hc.Memory != 0 || hc.MemorySwap != 0 {
		t.Fatalf("limits set for zero spec: %+v", hc.Resources)
	}
}

func TestHostConfigHardening(t *testing.T) {
	hc := hostConfig(sandbox.Spec{})
	if !reflect.DeepEqual(hc.CapDrop, []string{"ALL"}) {
		t.Fatalf("CapDrop = %v", hc.CapDrop)
	}
	if !slices.Contains(hc.SecurityOpt, "no-new-privileges:true") {
		t.Fatalf("SecurityOpt = %v", hc.SecurityOpt)
	}
	if hc.Init == nil || !*hc.Init {
		t.Fatal("Init is not true")
	}
	if len(hc.Binds) != 0 || len(hc.Mounts) != 0 || hc.ReadonlyRootfs {
		t.Fatalf("unexpected mounts or read-only rootfs: %+v", hc)
	}
}

func TestContainerConfigUserAndEnv(t *testing.T) {
	cfg := containerConfig(sandbox.Spec{Env: map[string]string{"B": "2", "A": "1"}})
	if cfg.User != "65532:65532" || cfg.WorkingDir != "" {
		t.Fatalf("User = %q, WorkingDir = %q", cfg.User, cfg.WorkingDir)
	}
	want := []string{"A=1", "B=2", "HOME=/tmp"}
	if !reflect.DeepEqual(cfg.Env, want) {
		t.Fatalf("Env = %v, want %v", cfg.Env, want)
	}
}

func TestContainerConfigIgnoresHostEnv(t *testing.T) {
	t.Setenv("ADLC_HOST_SECRET", "x")
	cfg := containerConfig(sandbox.Spec{})
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, "ADLC_HOST_SECRET") {
			t.Fatalf("host env leaked: %v", cfg.Env)
		}
	}
}

func TestContainerConfigKeepsHome(t *testing.T) {
	cfg := containerConfig(sandbox.Spec{Env: map[string]string{"HOME": "/home/x"}})
	if want := []string{"HOME=/home/x"}; !reflect.DeepEqual(cfg.Env, want) {
		t.Fatalf("Env = %v, want %v", cfg.Env, want)
	}
}
