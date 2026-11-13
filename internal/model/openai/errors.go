package openai

import (
	"context"
	"errors"
	"net"
	"net/url"

	oai "github.com/openai/openai-go/v3"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// classify turns an SDK error into a *model.Error. A cancelled or expired
// caller context is returned as is.
func classify(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var apiErr *oai.Error
	if errors.As(err, &apiErr) {
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
