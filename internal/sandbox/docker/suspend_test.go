package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
)

func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "a")
	if err := os.WriteFile(good, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		path    string
		want    string
		size    int64
		wantErr bool
	}{
		{"known bytes", good, "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", 3, false},
		{"missing file", filepath.Join(dir, "missing"), "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, n, err := fileSHA256(tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want || n != tt.size {
				t.Fatalf("got (%q, %d), want (%q, %d)", got, n, tt.want, tt.size)
			}
		})
	}
}

func TestDestroyAfterCopyKeepsTar(t *testing.T) {
	errBusy := errors.New("daemon busy")
	tests := []struct {
		name    string
		destroy error
		wantErr error
	}{
		{"destroyed", nil, nil},
		{"container already gone", fmt.Errorf("docker: destroy x: %w", sandbox.ErrNotFound), nil},
		{"destroy fails", fmt.Errorf("docker: destroy x: %w", errBusy), errBusy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tarPath := filepath.Join(t.TempDir(), "w.tar")
			if err := os.WriteFile(tarPath, []byte("tar"), 0o600); err != nil {
				t.Fatal(err)
			}
			want := sandbox.Snapshot{TarPath: tarPath, SHA256: "sha256:x"}
			destroy := func(context.Context, sandbox.Handle) error { return tt.destroy }
			got, err := destroyAfterCopy(context.Background(), destroy, sandbox.Handle{ID: "x"}, want)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err == nil && got != want {
				t.Fatalf("snapshot = %+v, want %+v", got, want)
			}
			if _, err := os.Stat(tarPath); err != nil {
				t.Fatalf("tar removed: %v", err)
			}
		})
	}
}
