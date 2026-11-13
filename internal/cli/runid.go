package cli

import (
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"time"
)

var runIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// newRunID returns "run-<UTC time as 20060102T150405Z>-<6 lowercase hex chars>".
func newRunID(now time.Time, random io.Reader) (string, error) {
	var b [3]byte
	if _, err := io.ReadFull(random, b[:]); err != nil {
		return "", fmt.Errorf("run: generate run id: %w", err)
	}
	return "run-" + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:]), nil
}

// validRunID reports whether s matches ^[A-Za-z0-9._-]{1,64}$.
func validRunID(s string) bool { return runIDRE.MatchString(s) }
