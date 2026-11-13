package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNewRunIDFormat(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	got, err := newRunID(now, bytes.NewReader([]byte{1, 2, 3}))
	if err != nil {
		t.Fatal(err)
	}
	if want := "run-20261113T120000Z-010203"; got != want {
		t.Fatalf("id = %q, want %q", got, want)
	}
}

func TestNewRunIDUsesUTC(t *testing.T) {
	now := time.Date(2026, 11, 13, 23, 30, 0, 0, time.FixedZone("x", 2*3600))
	got, err := newRunID(now, bytes.NewReader([]byte{0xab, 0xcd, 0xef}))
	if err != nil {
		t.Fatal(err)
	}
	if want := "run-20261113T213000Z-abcdef"; got != want {
		t.Fatalf("id = %q, want %q", got, want)
	}
}

func TestNewRunIDShortRandom(t *testing.T) {
	if _, err := newRunID(time.Now(), bytes.NewReader([]byte{1})); err == nil {
		t.Fatal("want error for short random source")
	}
}

func TestValidRunID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"run-1", true},
		{"a.b_c", true},
		{strings.Repeat("a", 64), true},
		{"", false},
		{"a b", false},
		{"x/y", false},
		{strings.Repeat("a", 65), false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := validRunID(tt.id); got != tt.want {
				t.Errorf("validRunID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}
