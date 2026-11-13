package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/buildinfo"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Execute(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestVersionText(t *testing.T) {
	code, out, _ := run(t, "version")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(out, "adlc ") {
		t.Errorf("output %q does not start with %q", out, "adlc ")
	}
}

func TestVersionJSON(t *testing.T) {
	code, out, _ := run(t, "version", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var info buildinfo.Info
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("invalid JSON %q: %v", out, err)
	}
	if info.Version == "" || info.GoVersion == "" || info.Platform == "" {
		t.Errorf("missing fields: %+v", info)
	}
}

func TestVersionUsesLinkTimeValues(t *testing.T) {
	old := buildinfo.Version
	t.Cleanup(func() { buildinfo.Version = old })
	buildinfo.Version = "v9.9.9"

	_, out, _ := run(t, "version")
	if !strings.Contains(out, "v9.9.9") {
		t.Errorf("output %q does not contain link-time version", out)
	}
}

func TestVersionRejectsArgs(t *testing.T) {
	code, _, errOut := run(t, "version", "extra")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "Error:") {
		t.Errorf("stderr %q has no error message", errOut)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, _ := run(t, "nope")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}
