package policy

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ArgMatcher matches one top-level field of a tool call's JSON arguments.
// Exactly one field is set. The argument must be a JSON string; a missing
// field, a non-string value, or arguments that are not a JSON object never match.
type ArgMatcher struct {
	Glob   string   // whole value; "*" = any run of characters except "/", "**" = any run, "?" = one character except "/"
	Prefix string   // strings.HasPrefix(value, Prefix)
	Regex  string   // RE2; must match the whole value (compiled as ^(?:Regex)$)
	In     []string // value equals one of these
}

// Check reports a matcher that does not set exactly one field, a bad glob or
// a bad regex. Its error has no "policy:" prefix: callers put it after a path.
func (m ArgMatcher) Check() error {
	n := 0
	for _, set := range []bool{m.Glob != "", m.Prefix != "", m.Regex != "", len(m.In) > 0} {
		if set {
			n++
		}
	}
	if n != 1 {
		return errors.New("set exactly one of glob, prefix, regex, in")
	}
	if m.Glob != "" {
		return CheckGlob(m.Glob)
	}
	if m.Regex != "" {
		if _, err := regexp.Compile(m.Regex); err != nil {
			return fmt.Errorf("bad regex %q: %w", m.Regex, err)
		}
	}
	return nil
}

// CheckGlob reports a glob this package cannot compile. Its error has no
// "policy:" prefix: callers put it after a path.
func CheckGlob(glob string) error {
	_, err := globRegexp(glob)
	return err
}

// globRegexp translates a glob into an anchored regular expression.
func globRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?s)^")
	rs := []rune(glob) // runes, not bytes: a multibyte character must stay valid UTF-8
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[', ']', '{', '}', '\\':
			return nil, fmt.Errorf("bad glob %q: %q is not supported (use *, ** or ?)", glob, string(c))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()), nil
}
