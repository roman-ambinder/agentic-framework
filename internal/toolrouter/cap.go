package toolrouter

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// DefaultMaxModelBytes is used when config does not set a limit.
const DefaultMaxModelBytes = 16 * 1024

// ForModel caps tool output for the model. If len(out) > max, it returns the
// first max bytes (cut back to a UTF-8 character boundary) followed by a
// "[output truncated]" line, and truncated is true. max <= 0 means
// DefaultMaxModelBytes. Invalid UTF-8 is replaced with U+FFFD.
func ForModel(out []byte, max int) (text string, truncated bool) {
	if max <= 0 {
		max = DefaultMaxModelBytes
	}
	if len(out) <= max {
		return strings.ToValidUTF8(string(out), "�"), false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	text = strings.ToValidUTF8(string(out[:cut]), "�")
	return text + fmt.Sprintf("\n[output truncated: showing %d of %d bytes]", cut, len(out)), true
}
