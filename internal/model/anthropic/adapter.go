// Package anthropic implements model.Adapter for the Anthropic Messages API
// using the official Go SDK. This minimal version does not stream.
package anthropic

import (
	"context"
	"fmt"
	"net/http"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// ProviderName is the value of Adapter.Provider and of Blob.Provider.
const ProviderName = "anthropic"

// Options configures New.
type Options struct {
	APIKey           string       // "" = the SDK reads ANTHROPIC_API_KEY from the environment
	BaseURL          string       // "" = SDK default; tests use an httptest URL
	HTTPClient       *http.Client // nil = SDK default
	MaxContextTokens int          // reported in Capabilities; 0 = unknown
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

// Capabilities reports tool calling and prompt caching, but no streaming.
func (a *Adapter) Capabilities() model.Caps {
	return model.Caps{ToolCalling: true, PromptCaching: true, Streaming: false, MaxContextTokens: a.opts.MaxContextTokens}
}

// Generate makes one non-streaming Messages API call.
func (a *Adapter) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	if req.MaxTokens <= 0 {
		return nil, fmt.Errorf("anthropic: MaxTokens must be > 0")
	}
	msgs, err := buildMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := buildTools(req.Tools)
	if err != nil {
		return nil, err
	}
	eff, err := effort(req.Effort)
	if err != nil {
		return nil, err
	}
	p := sdk.MessageNewParams{Model: req.Model, MaxTokens: int64(req.MaxTokens), Messages: msgs}
	if req.System != "" {
		p.System = []sdk.TextBlockParam{{Text: req.System}}
	}
	if len(tools) > 0 {
		p.Tools = tools
		p.ToolChoice = sdk.ToolChoiceUnionParam{OfAuto: &sdk.ToolChoiceAutoParam{}}
	}
	if eff != "" {
		p.OutputConfig = sdk.OutputConfigParam{Effort: eff}
	}
	msg, err := a.client.Messages.New(ctx, p)
	if err != nil {
		return nil, classify(ctx, err)
	}
	resp := toResponse(msg)
	resp.Provider = ProviderName
	resp.Model = req.Model
	return resp, nil
}

// CountTokens asks the API how many input tokens req uses.
func (a *Adapter) CountTokens(ctx context.Context, req *model.Request) (int64, error) {
	msgs, err := buildMessages(req.Messages)
	if err != nil {
		return 0, err
	}
	tools, err := buildTools(req.Tools)
	if err != nil {
		return 0, err
	}
	p := sdk.MessageCountTokensParams{Model: req.Model, Messages: msgs}
	if req.System != "" {
		p.System = sdk.MessageCountTokensParamsSystemUnion{OfTextBlockArray: []sdk.TextBlockParam{{Text: req.System}}}
	}
	for _, t := range tools {
		p.Tools = append(p.Tools, sdk.MessageCountTokensToolUnionParam{OfTool: t.OfTool})
	}
	res, err := a.client.Messages.CountTokens(ctx, p)
	if err != nil {
		return 0, classify(ctx, err)
	}
	return res.InputTokens, nil
}
