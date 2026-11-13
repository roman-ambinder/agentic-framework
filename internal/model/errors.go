package model

import (
	"errors"
	"fmt"
	"strings"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

// Error is returned by adapters for failed calls.
type Error struct {
	Class      adlcv1.ErrorClass // MODEL, RATE_LIMIT, TIMEOUT, ...
	HTTPStatus int               // 0 if none
	Retryable  bool
	Err        error
}

// Error formats as "model: <class> (http <status>): <Err>".
func (e *Error) Error() string {
	class := strings.ToLower(strings.TrimPrefix(e.Class.String(), "ERROR_CLASS_"))
	msg := "error"
	if e.Err != nil {
		msg = e.Err.Error()
	}
	if e.HTTPStatus == 0 {
		return fmt.Sprintf("model: %s: %s", class, msg)
	}
	return fmt.Sprintf("model: %s (http %d): %s", class, e.HTTPStatus, msg)
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error { return e.Err }

// IsRetryable reports whether err (or an *Error it wraps) is retryable.
// context.Canceled and context.DeadlineExceeded are not.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Retryable
	}
	return false
}

// ClassifyHTTP maps an HTTP status from a provider to an error class and
// whether the call may be retried (interfaces.md §2.1).
func ClassifyHTTP(status int) (class adlcv1.ErrorClass, retryable bool) {
	switch {
	case status == 429:
		return adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, true
	case status == 408 || status == 529:
		return adlcv1.ErrorClass_ERROR_CLASS_MODEL, true
	case status >= 400 && status <= 499:
		return adlcv1.ErrorClass_ERROR_CLASS_MODEL, false
	case status >= 500 && status <= 599:
		return adlcv1.ErrorClass_ERROR_CLASS_MODEL, true
	default:
		return adlcv1.ErrorClass_ERROR_CLASS_INTERNAL, false
	}
}
