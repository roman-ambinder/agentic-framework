package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
)

func TestApproveAndResumeHaveWorkspaceFlags(t *testing.T) {
	run := newRunCmd()
	for _, cmd := range []*cobra.Command{newApproveCmd(), newResumeCmd()} {
		for _, name := range []string{"workspace-out", "keep-sandbox"} {
			t.Run(cmd.Name()+"/"+name, func(t *testing.T) {
				got, want := cmd.Flags().Lookup(name), run.Flags().Lookup(name)
				if got == nil {
					t.Fatalf("adlc %s has no --%s", cmd.Name(), name)
				}
				if got.Usage != want.Usage || got.DefValue != want.DefValue {
					t.Errorf("--%s = %q (default %q), want as on adlc run: %q (default %q)",
						name, got.Usage, got.DefValue, want.Usage, want.DefValue)
				}
			})
		}
	}
}

func TestApproveKeepSandboxKeepsContainer(t *testing.T) {
	e := pausableRun(t, "", &sandboxtest.Pausable{})
	p := filepath.Join(e.dir, "done.json")
	if err := os.WriteFile(p, []byte(approveScript), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(e.dir, "elsewhere")
	o := approveOptions{runOptions: runOptions{agentPath: e.agent, db: e.db, fakeScript: p, workspaceOut: out, keepSandbox: true}, by: "bob"}
	var stdout bytes.Buffer
	if err := approveRun(context.Background(), o, "a-call_1", "r1", &stdout,
		func(context.Context) (sandboxLookup, error) { return e.b, nil }); err != nil {
		t.Fatalf("approve: %v, stdout %q", err, stdout.String())
	}
	if live := e.b.Live(); len(live) != 1 || !strings.Contains(stdout.String(), "sandbox: kept ") {
		t.Errorf("live %v, stdout %q; want the container kept", live, stdout.String())
	}
	if _, err := os.Stat(filepath.Join(out, "greeting.txt")); err != nil {
		t.Errorf("workspace not copied to --workspace-out: %v", err)
	}
}
