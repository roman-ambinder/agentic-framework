// Package router picks a model adapter per call and falls back to the next
// candidate when one fails with a retryable error.
package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// ProviderName is the value of Router.Provider.
const ProviderName = "router"

// ErrNoCandidates is returned by New for an empty list.
var ErrNoCandidates = errors.New("router: no candidates")

// Candidate is one adapter plus the model id to ask it for.
type Candidate struct {
	Adapter model.Adapter
	Model   string
}

// Router tries candidates in order. It implements model.Adapter.
type Router struct {
	candidates []Candidate
}

var _ model.Adapter = (*Router)(nil)

// New returns a Router over candidates, primary first.
func New(candidates ...Candidate) (*Router, error) {
	if len(candidates) == 0 {
		return nil, ErrNoCandidates
	}
	return &Router{candidates: append([]Candidate(nil), candidates...)}, nil
}

// Provider returns "router".
func (r *Router) Provider() string { return ProviderName }

// Generate calls the candidates in order and returns the first success. It
// falls back only on retryable errors; the response names the candidate that
// answered. It never modifies req.
func (r *Router) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	var last error
	for _, c := range r.candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		creq := *req
		creq.Model = c.Model
		resp, err := c.Adapter.Generate(ctx, &creq)
		if err == nil {
			resp.Provider = c.Adapter.Provider()
			resp.Model = c.Model
			return resp, nil
		}
		if !model.IsRetryable(err) {
			return nil, err
		}
		last = err
	}
	return nil, fmt.Errorf("router: all %d candidates failed: %w", len(r.candidates), last)
}

// CountTokens counts with the primary candidate's model.
func (r *Router) CountTokens(ctx context.Context, req *model.Request) (int64, error) {
	creq := *req
	creq.Model = r.candidates[0].Model
	return r.candidates[0].Adapter.CountTokens(ctx, &creq)
}

// Capabilities combines the candidates: tool calling only if all support it,
// caching and streaming as the primary reports, and the smallest non-zero
// context limit.
func (r *Router) Capabilities() model.Caps {
	first := r.candidates[0].Adapter.Capabilities()
	out := model.Caps{ToolCalling: true, PromptCaching: first.PromptCaching, Streaming: first.Streaming}
	for _, c := range r.candidates {
		caps := c.Adapter.Capabilities()
		out.ToolCalling = out.ToolCalling && caps.ToolCalling
		if n := caps.MaxContextTokens; n != 0 && (out.MaxContextTokens == 0 || n < out.MaxContextTokens) {
			out.MaxContextTokens = n
		}
	}
	return out
}
