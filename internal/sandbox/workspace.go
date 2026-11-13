package sandbox

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// WorkspaceTar returns a tar archive that, extracted at "/", creates
// /workspace with the contents of dir. dir == "" gives an archive with only
// the empty /workspace directory. Every entry is owned by uid:gid.
// Symlinks and special files are skipped (and counted in skipped).
// It returns an error wrapping ErrInvalidSpec if the regular files add up
// to more than maxBytes.
func WorkspaceTar(dir string, uid, gid int, maxBytes int64) (archive []byte, skipped int, err error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	root := &tar.Header{
		Name:     "workspace/",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
		Uid:      uid,
		Gid:      gid,
		ModTime:  time.Unix(0, 0),
	}
	if err := tw.WriteHeader(root); err != nil {
		return nil, 0, fmt.Errorf("sandbox: write workspace header: %w", err)
	}
	if dir != "" {
		w := &treeWriter{tw: tw, dir: dir, uid: uid, gid: gid, max: maxBytes}
		if err := filepath.WalkDir(dir, w.visit); err != nil {
			return nil, 0, err
		}
		skipped = w.skipped
	}
	if err := tw.Close(); err != nil {
		return nil, 0, fmt.Errorf("sandbox: close workspace archive: %w", err)
	}
	return buf.Bytes(), skipped, nil
}

type treeWriter struct {
	tw       *tar.Writer
	dir      string
	uid, gid int
	max      int64
	total    int64
	skipped  int
}

func (w *treeWriter) visit(path string, d fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return fmt.Errorf("sandbox: walk workspace: %w", walkErr)
	}
	if path == w.dir {
		return nil
	}
	rel, err := filepath.Rel(w.dir, path)
	if err != nil {
		return fmt.Errorf("sandbox: walk workspace: %w", err)
	}
	name := "workspace/" + filepath.ToSlash(rel)
	info, err := d.Info()
	if err != nil {
		return fmt.Errorf("sandbox: stat %s: %w", rel, err)
	}
	hdr := &tar.Header{Uid: w.uid, Gid: w.gid, ModTime: info.ModTime()}
	switch {
	case d.IsDir():
		hdr.Name, hdr.Typeflag, hdr.Mode = name+"/", tar.TypeDir, 0o755
		return w.writeHeader(hdr)
	case d.Type().IsRegular():
		w.total += info.Size()
		if w.total > w.max {
			return fmt.Errorf("%w: workspace is larger than %d bytes", ErrInvalidSpec, w.max)
		}
		hdr.Name, hdr.Typeflag, hdr.Mode, hdr.Size = name, tar.TypeReg, int64(info.Mode().Perm()), info.Size()
		return w.writeFile(hdr, path)
	default:
		w.skipped++ // symlink, socket, device: WalkDir does not descend into these
		return nil
	}
}

func (w *treeWriter) writeHeader(hdr *tar.Header) error {
	if err := w.tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("sandbox: write header %s: %w", hdr.Name, err)
	}
	return nil
}

func (w *treeWriter) writeFile(hdr *tar.Header, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sandbox: open %s: %w", path, err)
	}
	defer f.Close()
	if err := w.writeHeader(hdr); err != nil {
		return err
	}
	if _, err := io.CopyN(w.tw, f, hdr.Size); err != nil {
		return fmt.Errorf("sandbox: copy %s: %w", hdr.Name, err)
	}
	return nil
}
