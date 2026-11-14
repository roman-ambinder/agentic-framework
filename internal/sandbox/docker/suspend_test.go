package docker

import (
	"os"
	"path/filepath"
	"testing"
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
