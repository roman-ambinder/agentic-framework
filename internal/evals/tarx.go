package evals

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractTar unpacks the tar archive data into dest. Every entry must lie
// under stripDir (the base name of the copied path, as `docker cp` names
// entries); that prefix is removed. Symlinks and other special entries are
// skipped.
//
//nolint:unparam // stripDir is a parameter by design (brief); callers all pass the docker cp base name
func extractTar(data []byte, dest, stripDir string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("evals: extract: %w", err)
	}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("evals: read tar: %w", err)
		}
		rel, err := tarEntryPath(hdr.Name, stripDir)
		if err != nil {
			return err
		}
		if rel == "" {
			continue // the root itself
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			err = writeTarFile(target, tr, os.FileMode(hdr.Mode)&0o777)
		}
		if err != nil {
			return fmt.Errorf("evals: extract %q: %w", hdr.Name, err)
		}
	}
}

// tarEntryPath validates an entry name and returns it relative to stripDir
// ("" for the root itself).
func tarEntryPath(name, stripDir string) (string, error) {
	clean := path.Clean(name)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("evals: unsafe tar entry %q", name)
	}
	if clean == stripDir {
		return "", nil
	}
	if !strings.HasPrefix(clean, stripDir+"/") {
		return "", fmt.Errorf("evals: unexpected tar entry %q", name)
	}
	return strings.TrimPrefix(clean, stripDir+"/"), nil
}

func writeTarFile(target string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ExtractWorkspaceTar unpacks the archive CopyOut returns for the sandbox's
// /workspace into dest.
func ExtractWorkspaceTar(data []byte, dest string) error {
	return extractTar(data, dest, "workspace")
}
