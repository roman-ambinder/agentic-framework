package docker

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestSanitize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"run/1 x", "run-1-x"},
		{"Abc_1.2-z", "Abc_1.2-z"},
		{"", ""},
		{"a:b@c", "a-b-c"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := sanitize(tt.in); got != tt.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEnvListSorted(t *testing.T) {
	got := envList(map[string]string{"B": "2", "A": "1", "C": "x=y"})
	want := []string{"A=1", "B=2", "C=x=y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("envList = %v, want %v", got, want)
	}
	if got := envList(nil); len(got) != 0 {
		t.Fatalf("envList(nil) = %v", got)
	}
}

func TestLimitedBuffer(t *testing.T) {
	const limit = 10
	b := &limitedBuffer{limit: limit}
	chunk := make([]byte, limit)
	total := 0
	for range 3 {
		n, err := b.Write(chunk)
		if err != nil {
			t.Fatal(err)
		}
		total += n
	}
	if total != 3*limit {
		t.Fatalf("reported %d bytes written, want %d", total, 3*limit)
	}
	if len(b.Bytes()) != limit {
		t.Fatalf("kept %d bytes, want %d", len(b.Bytes()), limit)
	}
}

func TestNewUnavailable(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent.sock")
	_, err := New(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("New err = %v, want ErrUnavailable", err)
	}
}
