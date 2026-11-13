package policy

import (
	"strings"
	"testing"
)

func TestGlob(t *testing.T) {
	tests := []struct {
		glob, value string
		want        bool
	}{
		{"docs/**", "docs/a.md", true},
		{"docs/**", "docs/a/b.md", true},
		{"docs/**", "docs/", true},
		{"docs/**", "docs", false},
		{"docs/**", "src/docs/a.md", false},
		{"*.go", "main.go", true},
		{"*.go", "cmd/main.go", false},
		{"**.go", "cmd/main.go", true},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{"a?c", "ac", false},
		{"a.b", "axb", false},
		{"a.b", "a.b", true},
		{"fs__write_*", "fs__write_file", true},
		{"fs__write_*", "fs__read_file", false},
		{"fs__read_file", "fs__read_file", true},
		{"fs__read_file", "fs__read_files", false},
		{"é?", "éü", true},
	}
	for _, tt := range tests {
		t.Run(tt.glob+" "+tt.value, func(t *testing.T) {
			re, err := globRegexp(tt.glob)
			if err != nil {
				t.Fatal(err)
			}
			if got := re.MatchString(tt.value); got != tt.want {
				t.Errorf("glob %q on %q = %v, want %v", tt.glob, tt.value, got, tt.want)
			}
		})
	}
}

func TestCheckGlobRejects(t *testing.T) {
	tests := []struct{ glob, want string }{
		{"docs/[a]", `bad glob "docs/[a]": "[" is not supported (use *, ** or ?)`},
		{"{a,b}", `bad glob "{a,b}": "{" is not supported (use *, ** or ?)`},
		{`a\b`, `bad glob "a\\b": "\\" is not supported (use *, ** or ?)`},
		{"x]", `bad glob "x]": "]" is not supported (use *, ** or ?)`},
	}
	for _, tt := range tests {
		t.Run(tt.glob, func(t *testing.T) {
			err := CheckGlob(tt.glob)
			if err == nil || err.Error() != tt.want {
				t.Errorf("CheckGlob(%q) = %v, want %q", tt.glob, err, tt.want)
			}
		})
	}
	if err := CheckGlob("docs/**"); err != nil {
		t.Errorf("CheckGlob(valid) = %v", err)
	}
}

func TestArgMatcherCheck(t *testing.T) {
	const one = "set exactly one of glob, prefix, regex, in"
	tests := []struct {
		name string
		m    ArgMatcher
		want string // "" = nil; otherwise a prefix of the error
	}{
		{"none set", ArgMatcher{}, one},
		{"two set", ArgMatcher{Glob: "a", Prefix: "b"}, one},
		{"empty in", ArgMatcher{In: []string{}}, one},
		{"bad regex", ArgMatcher{Regex: "("}, `bad regex "(": error parsing regexp`},
		{"bad glob", ArgMatcher{Glob: "a["}, `bad glob "a["`},
		{"glob", ArgMatcher{Glob: "a*"}, ""},
		{"prefix", ArgMatcher{Prefix: "a"}, ""},
		{"regex", ArgMatcher{Regex: "a+"}, ""},
		{"in", ArgMatcher{In: []string{"a"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.m.Check()
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("Check() = %v, want nil", err)
			case tt.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.want)):
				t.Errorf("Check() = %v, want prefix %q", err, tt.want)
			}
		})
	}
}
