// Package openai implements model.Adapter for the OpenAI Chat Completions API
// using the official Go SDK. It also works with OpenAI-compatible servers
// through Config.BaseURL. It does not stream.
package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	oai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// ProviderName is the value of Adapter.Provider.
const ProviderName = "openai"

// Config configures the adapter.
type Config struct {
	APIKey           string        // "" = the SDK reads OPENAI_API_KEY
	BaseURL          string        // "" = SDK default; set for compatible servers
	HTTPClient       *http.Client  // nil = SDK default; tests pass the test server's client
	MaxContextTokens int           // reported by Capabilities; 0 = unknown
	RequestTimeout   time.Duration // 0 = none (ctx still applies)
}

// Adapter implements model.Adapter for OpenAI Chat Completions.
type Adapter struct {
	cfg    Config
	client oai.Client
}

var _ model.Adapter = (*Adapter)(nil)

// New builds an adapter. It never fails today but returns an error so
// validation can be added without changing callers. The SDK's own retries are
// always off: the harness retries.
func New(cfg Config) (*Adapter, error) {
	opts := []option.RequestOption{option.WithMaxRetries(0)}
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	if cfg.RequestTimeout != 0 {
		opts = append(opts, option.WithRequestTimeout(cfg.RequestTimeout))
	}
	return &Adapter{cfg: cfg, client: oai.NewClient(opts...)}, nil
}

// Provider returns ProviderName.
func (a *Adapter) Provider() string { return ProviderName }

// Capabilities reports tool calling and prompt caching (automatic on the
// OpenAI side). Streaming is not supported.
func (a *Adapter) Capabilities() model.Caps {
	return model.Caps{
		ToolCalling:      true,
		PromptCaching:    true,
		Streaming:        false,
		MaxContextTokens: a.cfg.MaxContextTokens,
	}
}

// Generate makes one Chat Completions call.
func (a *Adapter) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	p, err := buildParams(req)
	if err != nil {
		return nil, err
	}
	c, err := a.client.Chat.Completions.New(ctx, p)
	if err != nil {
		return nil, classify(ctx, err)
	}
	resp, err := toResponse(c)
	if err != nil {
		return nil, err
	}
	resp.Provider = ProviderName
	resp.Model = req.Model
	return resp, nil
}

// CountTokens estimates the input tokens of req. Chat Completions has no
// token counting endpoint, so this marshals the request and assumes about 4
// bytes per token. It is an estimate only.
func (a *Adapter) CountTokens(_ context.Context, req *model.Request) (int64, error) {
	p, err := buildParams(req)
	if err != nil {
		return 0, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return 0, err
	}
	return int64((len(b) + 3) / 4), nil
}
