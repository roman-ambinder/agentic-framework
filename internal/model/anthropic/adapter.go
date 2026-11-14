// Package anthropic implements model.Adapter for the Anthropic Messages API
// using the official Go SDK. Generate always streams internally and returns
// one complete response.
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// ProviderName is the value of Adapter.Provider and of Blob.Provider.
const ProviderName = "anthropic"

// Options configures New.
type Options struct {
	APIKey           string       // "" = the SDK's default chain: ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, then a signed-in profile
	BaseURL          string       // "" = SDK default; tests use an httptest URL
	HTTPClient       *http.Client // nil = SDK default
	MaxContextTokens int          // reported in Capabilities; 0 = unknown
	DisableCaching   bool         // true = send no cache_control breakpoints
}

// Adapter calls the Anthropic Messages API.
type Adapter struct {
	client sdk.Client
	opts   Options
}

var _ model.Adapter = (*Adapter)(nil)

// New returns an Adapter. The SDK's own retries are always off: the harness
// retries (task 0.5.3).
func New(opts Options) *Adapter {
	ro := []option.RequestOption{option.WithMaxRetries(0)}
	if opts.APIKey != "" {
		ro = append(ro, option.WithAPIKey(opts.APIKey))
	}
	if opts.BaseURL != "" {
		ro = append(ro, option.WithBaseURL(opts.BaseURL))
	}
	if opts.HTTPClient != nil {
		ro = append(ro, option.WithHTTPClient(opts.HTTPClient))
	}
	return &Adapter{client: sdk.NewClient(ro...), opts: opts}
}

// Provider returns ProviderName.
func (a *Adapter) Provider() string { return ProviderName }

// Capabilities reports tool calling, streaming and (unless disabled) prompt caching.
func (a *Adapter) Capabilities() model.Caps {
	return model.Caps{
		ToolCalling:      true,
		PromptCaching:    !a.opts.DisableCaching,
		Streaming:        true,
		MaxContextTokens: a.opts.MaxContextTokens,
	}
}

// Generate makes one streaming Messages API call and returns the complete
// response once the stream ends.
func (a *Adapter) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	if req.MaxTokens <= 0 {
		return nil, fmt.Errorf("anthropic: MaxTokens must be > 0")
	}
	params, err := a.buildParams(req)
	if err != nil {
		return nil, err
	}
	if !a.opts.DisableCaching {
		applyCaching(&params)
	}
	stream := a.client.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()
	msg := sdk.Message{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return nil, &model.Error{
				Class: adlcv1.ErrorClass_ERROR_CLASS_MODEL,
				Err:   fmt.Errorf("anthropic: accumulate stream: %w", err),
			}
		}
	}
	if err := stream.Err(); err != nil {
		return nil, classify(ctx, err)
	}
	if msg.StopReason == "" {
		return nil, &model.Error{
			Class:     adlcv1.ErrorClass_ERROR_CLASS_MODEL,
			Retryable: true,
			Err:       errors.New("anthropic: stream ended before the message was complete"),
		}
	}
	resp := toResponse(&msg)
	resp.Provider = ProviderName
	resp.Model = req.Model
	return resp, nil
}

// applyCaching puts a cache breakpoint on the last system block (this caches
// tools and system) and turns on top-level automatic caching for the messages.
func applyCaching(p *sdk.MessageNewParams) {
	if len(p.System) > 0 {
		p.System[len(p.System)-1].CacheControl = sdk.NewCacheControlEphemeralParam()
	}
	p.CacheControl = sdk.NewCacheControlEphemeralParam()
}

// CountTokens asks the API how many input tokens req uses.
func (a *Adapter) CountTokens(ctx context.Context, req *model.Request) (int64, error) {
	params, err := a.buildParams(req)
	if err != nil {
		return 0, err
	}
	cp := sdk.MessageCountTokensParams{
		Model:        params.Model,
		Messages:     params.Messages,
		OutputConfig: params.OutputConfig,
		Thinking:     params.Thinking,
	}
	if len(params.System) > 0 {
		cp.System = sdk.MessageCountTokensParamsSystemUnion{OfTextBlockArray: params.System}
	}
	for _, t := range params.Tools {
		if t.OfTool != nil {
			cp.Tools = append(cp.Tools, sdk.MessageCountTokensToolUnionParam{OfTool: t.OfTool})
		}
	}
	res, err := a.client.Messages.CountTokens(ctx, cp)
	if err != nil {
		return 0, classify(ctx, err)
	}
	return res.InputTokens, nil
}
