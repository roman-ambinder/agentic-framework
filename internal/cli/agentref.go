package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/controlplane"
)

// errAgentDirNeedsRef is returned when --agent-dir is given with an agent file.
var errAgentDirNeedsRef = errors.New("run: --agent-dir only applies to a registered agent (NAME@N or NAME@ENV)")

// resolveAgentRef turns a registry reference in o.agentPath into a file: it
// writes the version's files into a new temporary directory and returns
// options whose agentPath is <dir>/agent.yaml and whose agentDir is the
// version's SourceDir unless --agent-dir was given. cleanup removes the
// directory. A path that exists on disk is always a file.
func resolveAgentRef(ctx context.Context, o runOptions) (out runOptions, cleanup func(), err error) {
	noop := func() {}
	if _, err := os.Stat(o.agentPath); err == nil {
		return o, noop, fileOnly(o)
	}
	ref, err := controlplane.ParseRef(o.agentPath)
	if err != nil {
		// the normal load reports the missing file
		return o, noop, fileOnly(o)
	}
	store, err := openExistingControlDB(ctx, o.controlDB)
	if err != nil {
		return o, noop, err
	}
	defer store.Close()
	v, err := store.Resolve(ctx, ref)
	if err != nil {
		return o, noop, err
	}
	dir, err := os.MkdirTemp("", "adlc-agent-")
	if err != nil {
		return o, noop, fmt.Errorf("run: %s: create temporary directory: %w", v.Ref(), err)
	}
	if err := materialize(dir, v); err != nil {
		_ = os.RemoveAll(dir)
		return o, noop, err
	}
	o.agentPath = filepath.Join(dir, agentconfig.AgentFileName)
	if o.agentDir == "" {
		o.agentDir = v.SourceDir
	}
	return o, func() { _ = os.RemoveAll(dir) }, nil
}

// fileOnly refuses --agent-dir for a plain agent file, else returns nil.
func fileOnly(o runOptions) error {
	if o.agentDir != "" {
		return errAgentDirNeedsRef
	}
	return nil
}

// materialize writes the files of v below dir.
func materialize(dir string, v controlplane.AgentVersion) error {
	for _, f := range v.Files {
		rel := filepath.FromSlash(f.Path)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("run: %s: stored file %q is not a relative path", v.Ref(), f.Path)
		}
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("run: %s: %w", v.Ref(), err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			return fmt.Errorf("run: %s: %w", v.Ref(), err)
		}
	}
	return nil
}
