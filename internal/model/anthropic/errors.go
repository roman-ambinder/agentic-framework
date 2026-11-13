package anthropic

import (
	"context"
	"errors"
	"net"
	"net/url"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// classify turns an SDK error into a *model.Error. A cancelled or expired
// caller context is returned as is.
func classify(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode >= 200 && apiErr.StatusCode <= 299 {
			return classifyStreamError(apiErr, err)
		}
		class, retryable := model.ClassifyHTTP(apiErr.StatusCode)
		return &model.Error{Class: class, HTTPStatus: apiErr.StatusCode, Retryable: retryable, Err: err}
	}
	var netErr net.Error
	var urlErr *url.Error
	if errors.As(err, &netErr) || errors.As(err, &urlErr) {
		return &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_TIMEOUT, Retryable: true, Err: err}
	}
	return &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_MODEL, Err: err}
}

// classifyStreamError handles an error event that arrives inside a 200
// response stream: the SDK reports it as an *sdk.Error with a 2xx status, so
// the status says nothing and the error type decides.
func classifyStreamError(apiErr *sdk.Error, err error) error {
	switch string(apiErr.Type()) {
	case "rate_limit_error":
		return &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, Retryable: true, Err: err}
	case "overloaded_error", "api_error":
		return &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_MODEL, Retryable: true, Err: err}
	default:
		return &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_MODEL, Err: err}
	}
}
