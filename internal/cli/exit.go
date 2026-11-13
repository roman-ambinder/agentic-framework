package cli

import "fmt"

// exitError makes Execute return code without (if err is nil) printing "Error:".
type exitError struct {
	code int
	err  error
}

// Error returns the wrapped error's text, or "exit code N" when there is none.
func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit code %d", e.code)
}

// Unwrap returns the wrapped error.
func (e *exitError) Unwrap() error { return e.err }
