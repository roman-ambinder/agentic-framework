package evals

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

type tarEntry struct {
	name, body, link string
	typ              byte
}

func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Typeflag: e.typ, Mode: 0o644, Linkname: e.link}
		if e.typ == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if e.typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractTarStripsDir(t *testing.T) {
	data := buildTar(t, []tarEntry{
		{name: "workspace/", typ: tar.TypeDir},
		{name: "workspace/a.txt", body: "A", typ: tar.TypeReg},
		{name: "workspace/d/b.txt", body: "B", typ: tar.TypeReg},
	})
	dest := filepath.Join(t.TempDir(), "out")
	if err := extractTar(data, dest, "workspace"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a.txt": "A", "d/b.txt": "B"} {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "workspace")); err == nil {
		t.Error("workspace directory was not stripped")
	}
}

func TestExtractTarRejectsUnsafe(t *testing.T) {
	for _, name := range []string{"workspace/../x", "/etc/passwd", "other/a.txt", "../x", "workspace/a/../../x"} {
		t.Run(name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "out")
			data := buildTar(t, []tarEntry{{name: name, body: "x", typ: tar.TypeReg}})
			if err := extractTar(data, dest, "workspace"); err == nil {
				t.Errorf("entry %q was accepted", name)
			}
		})
	}
}

func TestExtractTarSkipsSymlinks(t *testing.T) {
	data := buildTar(t, []tarEntry{
		{name: "workspace/ln", link: "/etc/passwd", typ: tar.TypeSymlink},
		{name: "workspace/ok.txt", body: "ok", typ: tar.TypeReg},
	})
	dest := filepath.Join(t.TempDir(), "out")
	if err := extractTar(data, dest, "workspace"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "ln")); err == nil {
		t.Error("symlink was extracted")
	}
	if _, err := os.Stat(filepath.Join(dest, "ok.txt")); err != nil {
		t.Error(err)
	}
}
