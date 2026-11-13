package agentconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// AgentFileName is the path of the agent file itself in Agent.Sources,
// whatever the file was called on disk (renaming a file is not a new version).
const AgentFileName = "agent.yaml"

// SourceFile is one file an agent definition is made of.
type SourceFile struct {
	Path string // slash-separated; AgentFileName, or system_file as written in the agent file
	Data []byte // exact bytes read from disk
}

// DigestFiles returns "sha256:" + lowercase hex of the content digest of files.
func DigestFiles(files []SourceFile) string {
	sorted := slices.Clone(files)
	slices.SortStableFunc(sorted, func(a, b SourceFile) int {
		switch {
		case a.Path == AgentFileName && b.Path != AgentFileName:
			return -1
		case b.Path == AgentFileName && a.Path != AgentFileName:
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	h := sha256.New()
	for _, f := range sorted {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write([]byte(strconv.Itoa(len(f.Data))))
		h.Write([]byte{0})
		h.Write(f.Data)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Digest returns DigestFiles(a.Sources).
func (a *Agent) Digest() string { return DigestFiles(a.Sources) }

// LoadAt is Load, except that Agent.Dir — where tool and hook commands run
// and sandbox.workspace is resolved — is workDir (made absolute) instead of
// the file's directory. system_file is still read relative to the file.
func LoadAt(path, workDir string) (*Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agentconfig: %w", err)
	}
	abs, _ := filepath.Abs(path)
	wd, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("agentconfig: %s: %w", path, err)
	}
	a, err := parse(data, filepath.Dir(abs), wd)
	if err != nil {
		return nil, fmt.Errorf("agentconfig: %s: %w", path, err)
	}
	return a, nil
}
