package toolrouter

import (
	"strings"
	"testing"
)

func TestForModelShortOutputUnchanged(t *testing.T) {
	got, truncated := ForModel([]byte("hello"), 10)
	if got != "hello" || truncated {
		t.Fatalf("ForModel = %q, %v; want \"hello\", false", got, truncated)
	}
}

func TestForModelExactLimit(t *testing.T) {
	got, truncated := ForModel([]byte("0123456789"), 10)
	if got != "0123456789" || truncated {
		t.Fatalf("ForModel = %q, %v; want unchanged, false", got, truncated)
	}
}

func TestForModelTruncates(t *testing.T) {
	in := []byte("abcdefghijklmnopqrst") // 20 bytes
	got, truncated := ForModel(in, 10)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	wantPrefix := "abcdefghij"
	wantSuffix := "\n[output truncated: showing 10 of 20 bytes]"
	if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("ForModel = %q; want prefix %q and suffix %q", got, wantPrefix, wantSuffix)
	}
}

func TestForModelDoesNotSplitRunes(t *testing.T) {
	in := []byte("aé" + strings.Repeat("x", 10)) // a(1) é(2) x*10 = 13 bytes
	got, truncated := ForModel(in, 2)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if !strings.HasPrefix(got, "a\n[") {
		t.Fatalf("ForModel = %q; want shown part \"a\"", got)
	}
	if !strings.Contains(got, "showing 1 of 13 bytes") {
		t.Fatalf("ForModel = %q; want marker \"showing 1 of 13 bytes\"", got)
	}
}

func TestForModelInvalidUTF8Replaced(t *testing.T) {
	got, truncated := ForModel([]byte{'a', 0xff, 'b'}, 10)
	if truncated || got != "a�b" {
		t.Fatalf("ForModel = %q, %v; want \"a\\uFFFDb\", false", got, truncated)
	}
}

func TestForModelDefaultMax(t *testing.T) {
	in := make([]byte, 20000)
	for i := range in {
		in[i] = 'x'
	}
	got, truncated := ForModel(in, 0) // 0 means DefaultMaxModelBytes
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if !strings.Contains(got, "showing 16384 of 20000 bytes") {
		t.Fatalf("marker missing in %q", got[len(got)-80:])
	}
}
