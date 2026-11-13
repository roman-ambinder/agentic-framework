package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

func TestClassifyHTTP(t *testing.T) {
	const (
		rate  = adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT
		mod   = adlcv1.ErrorClass_ERROR_CLASS_MODEL
		inter = adlcv1.ErrorClass_ERROR_CLASS_INTERNAL
	)
	tests := []struct {
		status int
		class  adlcv1.ErrorClass
		retry  bool
	}{
		{429, rate, true},
		{408, mod, true},
		{500, mod, true},
		{502, mod, true},
		{503, mod, true},
		{504, mod, true},
		{529, mod, true},
		{400, mod, false},
		{401, mod, false},
		{404, mod, false},
		{501, mod, true},
		{0, inter, false},
		{200, inter, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			class, retry := ClassifyHTTP(tt.status)
			if class != tt.class || retry != tt.retry {
				t.Fatalf("ClassifyHTTP(%d) = %v, %v; want %v, %v", tt.status, class, retry, tt.class, tt.retry)
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{"with status", &Error{Class: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, HTTPStatus: 429, Err: errors.New("slow down")}, "model: rate_limit (http 429): slow down"},
		{"no status", &Error{Class: adlcv1.ErrorClass_ERROR_CLASS_TIMEOUT, Err: errors.New("late")}, "model: timeout: late"},
		{"nil err", &Error{Class: adlcv1.ErrorClass_ERROR_CLASS_MODEL}, "model: model: error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	if !errors.Is(&Error{Err: io.EOF}, io.EOF) {
		t.Fatal("errors.Is did not find io.EOF")
	}
}

func TestIsRetryable(t *testing.T) {
	retry := &Error{Retryable: true, Err: io.EOF}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"retryable", retry, true},
		{"wrapped", fmt.Errorf("x: %w", retry), true},
		{"not retryable", &Error{Err: io.EOF}, false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"plain", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryable(tt.err); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
