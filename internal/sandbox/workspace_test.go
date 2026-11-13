package sandbox

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type entry struct {
	hdr  tar.Header
	body string
}

func readTar(t *testing.T, data []byte) []entry {
	t.Helper()
	var out []entry
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, entry{hdr: *hdr, body: string(body)})
	}
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceTarEmptyDir(t *testing.T) {
	data, skipped, err := WorkspaceTar("", 65532, 65532, 1<<20)
	if err != nil || skipped != 0 {
		t.Fatalf("WorkspaceTar = %v, skipped %d", err, skipped)
	}
	es := readTar(t, data)
	if len(es) != 1 {
		t.Fatalf("got %d entries, want 1", len(es))
	}
	h := es[0].hdr
	if h.Name != "workspace/" || h.Typeflag != tar.TypeDir || h.Uid != 65532 || h.Gid != 65532 {
		t.Fatalf("entry = %+v", h)
	}
}

func TestWorkspaceTarContents(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "hello", 0o644)
	write(t, filepath.Join(dir, "sub", "run.sh"), "#!/bin/sh\n", 0o755)
	data, skipped, err := WorkspaceTar(dir, 65532, 65532, 1<<20)
	if err != nil || skipped != 0 {
		t.Fatalf("WorkspaceTar = %v, skipped %d", err, skipped)
	}
	es := readTar(t, data)
	type want struct {
		name string
		typ  byte
		mode int64
		body string
	}
	wants := []want{
		{"workspace/", tar.TypeDir, 0o755, ""},
		{"workspace/a.txt", tar.TypeReg, 0o644, "hello"},
		{"workspace/sub/", tar.TypeDir, 0o755, ""},
		{"workspace/sub/run.sh", tar.TypeReg, 0o755, "#!/bin/sh\n"},
	}
	var got, exp []want
	for _, e := range es {
		got = append(got, want{e.hdr.Name, e.hdr.Typeflag, e.hdr.Mode, e.body})
		if e.hdr.Uid != 65532 || e.hdr.Gid != 65532 {
			t.Errorf("%s owner = %d:%d", e.hdr.Name, e.hdr.Uid, e.hdr.Gid)
		}
	}
	exp = wants
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("entries = %+v\nwant %+v", got, exp)
	}
}

func TestWorkspaceTarSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "x", 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	data, skipped, err := WorkspaceTar(dir, 1, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	for _, e := range readTar(t, data) {
		if e.hdr.Name == "workspace/link" {
			t.Fatal("symlink is in the archive")
		}
	}
}

func TestWorkspaceTarTooLarge(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "big"), "01234567890123456789", 0o644)
	_, _, err := WorkspaceTar(dir, 1, 1, 10)
	if !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("err = %v, want ErrInvalidSpec", err)
	}
}

func TestWorkspaceTarDoesNotChangeDir(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	write(t, a, "hello", 0o644)
	old := time.Unix(1_000_000, 0)
	if err := os.Chtimes(a, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WorkspaceTar(dir, 1, 1, 1<<20); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(a)
	if err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("stat = %v, %v; want mod time %v", info, err, old)
	}
	if b, _ := os.ReadFile(a); string(b) != "hello" {
		t.Fatalf("content = %q", b)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Fatalf("dir has %d entries, want 1", len(es))
	}
}
