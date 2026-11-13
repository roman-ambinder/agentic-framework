package router

import (
	"context"
	"errors"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// answer is a scripted successful turn with the given text.
func answer(text string) model.FakeTurn {
	return model.FakeTurn{Response: &model.Response{
		Message: model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.TextPart(text)}},
		Finish:  adlcv1.FinishReason_FINISH_REASON_END_TURN,
	}}
}

// refusal is a scripted REFUSAL turn.
func refusal() model.FakeTurn {
	return model.FakeTurn{Response: &model.Response{Finish: adlcv1.FinishReason_FINISH_REASON_REFUSAL}}
}

// rateLimited is a retryable 429 error.
func rateLimited() model.FakeTurn {
	return model.FakeTurn{Err: &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, HTTPStatus: 429, Retryable: true}}
}

// badRequest is a permanent 400 error.
func badRequest() model.FakeTurn {
	return model.FakeTurn{Err: &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_MODEL, HTTPStatus: 400, Retryable: false}}
}

// text returns the text of the response's message.
func text(resp *model.Response) string { return resp.Message.Text() }

// capsAdapter is an adapter that reports fixed capabilities and token counts.
type capsAdapter struct {
	caps   model.Caps
	tokens int64
	seen   []string // model ids of CountTokens requests
}

func (c *capsAdapter) Provider() string { return "caps" }
func (c *capsAdapter) Generate(context.Context, *model.Request) (*model.Response, error) {
	return nil, errors.New("not used")
}

func (c *capsAdapter) CountTokens(_ context.Context, req *model.Request) (int64, error) {
	c.seen = append(c.seen, req.Model)
	return c.tokens, nil
}
func (c *capsAdapter) Capabilities() model.Caps { return c.caps }

// stubAdapter counts calls and always returns err.
type stubAdapter struct {
	err   error
	calls int
}

func (s *stubAdapter) Provider() string { return "stub" }
func (s *stubAdapter) Generate(context.Context, *model.Request) (*model.Response, error) {
	s.calls++
	return nil, s.err
}
func (s *stubAdapter) CountTokens(context.Context, *model.Request) (int64, error) { return 0, nil }
func (s *stubAdapter) Capabilities() model.Caps                                   { return model.Caps{} }

func TestNewRejectsEmpty(t *testing.T) {
	_, err := New()
	if !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("err = %v, want ErrNoCandidates", err)
	}
}

func TestPrimaryAnswers(t *testing.T) {
	primary := model.NewFake(answer("from primary"))
	fallback := model.NewFake()
	r, err := New(Candidate{primary, "m1"}, Candidate{fallback, "m2"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := r.Generate(context.Background(), &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "fake" || resp.Model != "m1" {
		t.Errorf("provider/model = %q/%q, want fake/m1", resp.Provider, resp.Model)
	}
	if n := len(fallback.Requests()); n != 0 {
		t.Errorf("fallback got %d requests, want 0", n)
	}
}

func TestFallbackOnRetryableError(t *testing.T) {
	primary := model.NewFake(rateLimited())
	fallback := model.NewFake(answer("from fallback"))
	r, _ := New(Candidate{primary, "m1"}, Candidate{fallback, "m2"})
	resp, err := r.Generate(context.Background(), &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if text(resp) != "from fallback" || resp.Model != "m2" {
		t.Errorf("response = %q model %q, want fallback answer with m2", text(resp), resp.Model)
	}
	if len(primary.Requests()) != 1 || len(fallback.Requests()) != 1 {
		t.Errorf("requests = %d/%d, want 1/1", len(primary.Requests()), len(fallback.Requests()))
	}
	if got := fallback.Requests()[0].Model; got != "m2" {
		t.Errorf("fallback request model = %q, want m2", got)
	}
}

func TestNoFallbackOnPermanentError(t *testing.T) {
	primary := model.NewFake(badRequest())
	fallback := model.NewFake(answer("unused"))
	r, _ := New(Candidate{primary, "m1"}, Candidate{fallback, "m2"})
	_, err := r.Generate(context.Background(), &model.Request{})
	var me *model.Error
	if !errors.As(err, &me) || me.HTTPStatus != 400 {
		t.Fatalf("err = %v, want model error with HTTP 400", err)
	}
	if n := len(fallback.Requests()); n != 0 {
		t.Errorf("fallback got %d requests, want 0", n)
	}
}

func TestNoFallbackOnRefusal(t *testing.T) {
	primary := model.NewFake(refusal())
	fallback := model.NewFake(answer("unused"))
	r, _ := New(Candidate{primary, "m1"}, Candidate{fallback, "m2"})
	resp, err := r.Generate(context.Background(), &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Finish != adlcv1.FinishReason_FINISH_REASON_REFUSAL {
		t.Errorf("finish = %v, want REFUSAL", resp.Finish)
	}
	if n := len(fallback.Requests()); n != 0 {
		t.Errorf("fallback got %d requests, want 0", n)
	}
}

func TestAllCandidatesFail(t *testing.T) {
	r, _ := New(
		Candidate{model.NewFake(rateLimited()), "m1"},
		Candidate{model.NewFake(rateLimited()), "m2"},
	)
	_, err := r.Generate(context.Background(), &model.Request{})
	if err == nil || !strings.HasPrefix(err.Error(), "router: all 2 candidates failed:") {
		t.Fatalf("err = %v, want prefix %q", err, "router: all 2 candidates failed:")
	}
	if !model.IsRetryable(err) {
		t.Errorf("IsRetryable(%v) = false, want true", err)
	}
}

func TestCancelledContextStops(t *testing.T) {
	// The stub ignores ctx and returns a retryable error, so only the router's
	// own check can stop the second candidate from being called.
	retry := &model.Error{Class: adlcv1.ErrorClass_ERROR_CLASS_RATE_LIMIT, HTTPStatus: 429, Retryable: true}
	primary := &stubAdapter{err: retry}
	fallback := &stubAdapter{err: retry}
	r, _ := New(Candidate{primary, "m1"}, Candidate{fallback, "m2"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Generate(ctx, &model.Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if primary.calls+fallback.calls != 0 {
		t.Errorf("candidates were called after cancellation: primary %d, fallback %d", primary.calls, fallback.calls)
	}
}

func TestRequestNotModified(t *testing.T) {
	primary := model.NewFake(rateLimited())
	fallback := model.NewFake(answer("ok"))
	r, _ := New(Candidate{primary, "m1"}, Candidate{fallback, "m2"})
	req := &model.Request{Model: "caller-model"}
	if _, err := r.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "caller-model" {
		t.Errorf("caller's req.Model = %q, want unchanged", req.Model)
	}
}

func TestEveryCallStartsAtPrimary(t *testing.T) {
	primary := model.NewFake(rateLimited(), answer("primary"))
	fallback := model.NewFake(answer("fallback"))
	r, _ := New(Candidate{primary, "m1"}, Candidate{fallback, "m2"})
	ctx := context.Background()

	first, err := r.Generate(ctx, &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if text(first) != "fallback" || first.Model != "m2" {
		t.Errorf("call 1 = %q model %q, want fallback/m2", text(first), first.Model)
	}
	second, err := r.Generate(ctx, &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if text(second) != "primary" || second.Model != "m1" {
		t.Errorf("call 2 = %q model %q, want primary/m1", text(second), second.Model)
	}
}

func TestCapabilitiesCombine(t *testing.T) {
	tests := []struct {
		name string
		a, b model.Caps
		want model.Caps
	}{
		{
			name: "smallest non-zero context wins",
			a:    model.Caps{ToolCalling: true, MaxContextTokens: 200},
			b:    model.Caps{ToolCalling: true, MaxContextTokens: 100},
			want: model.Caps{ToolCalling: true, MaxContextTokens: 100},
		},
		{
			name: "zero means unknown and is ignored",
			a:    model.Caps{ToolCalling: true, MaxContextTokens: 0},
			b:    model.Caps{ToolCalling: true, MaxContextTokens: 50},
			want: model.Caps{ToolCalling: true, MaxContextTokens: 50},
		},
		{
			name: "all zero stays zero",
			a:    model.Caps{ToolCalling: true},
			b:    model.Caps{ToolCalling: true},
			want: model.Caps{ToolCalling: true},
		},
		{
			name: "tool calling false if any candidate lacks it",
			a:    model.Caps{ToolCalling: true},
			b:    model.Caps{ToolCalling: false},
			want: model.Caps{ToolCalling: false},
		},
		{
			name: "caching and streaming come from the primary",
			a:    model.Caps{ToolCalling: true, PromptCaching: true, Streaming: true},
			b:    model.Caps{ToolCalling: true},
			want: model.Caps{ToolCalling: true, PromptCaching: true, Streaming: true},
		},
		{
			name: "caching from a secondary is not reported",
			a:    model.Caps{ToolCalling: true},
			b:    model.Caps{ToolCalling: true, PromptCaching: true, Streaming: true},
			want: model.Caps{ToolCalling: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _ := New(
				Candidate{&capsAdapter{caps: tt.a}, "m1"},
				Candidate{&capsAdapter{caps: tt.b}, "m2"},
			)
			if got := r.Capabilities(); got != tt.want {
				t.Errorf("Capabilities() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCountTokensUsesPrimary(t *testing.T) {
	primary := &capsAdapter{tokens: 42}
	fallback := &capsAdapter{tokens: 7}
	r, _ := New(Candidate{primary, "m1"}, Candidate{fallback, "m2"})
	req := &model.Request{Model: "caller-model"}
	got, err := r.CountTokens(context.Background(), req)
	if err != nil || got != 42 {
		t.Fatalf("CountTokens = %d, %v; want 42, nil", got, err)
	}
	if len(primary.seen) != 1 || primary.seen[0] != "m1" {
		t.Errorf("primary saw models %v, want [m1]", primary.seen)
	}
	if len(fallback.seen) != 0 {
		t.Errorf("fallback was asked to count tokens")
	}
	if req.Model != "caller-model" {
		t.Errorf("caller's req.Model = %q, want unchanged", req.Model)
	}
}

// bareAdapter answers every call with an empty response that names no
// provider or model, so only the router can fill them in.
type bareAdapter struct{}

func (bareAdapter) Provider() string { return "bare" }
func (bareAdapter) Generate(context.Context, *model.Request) (*model.Response, error) {
	return &model.Response{}, nil
}
func (bareAdapter) CountTokens(context.Context, *model.Request) (int64, error) { return 0, nil }
func (bareAdapter) Capabilities() model.Caps                                   { return model.Caps{} }

func TestRouterSetsServedModel(t *testing.T) {
	r, _ := New(Candidate{bareAdapter{}, "m9"})
	resp, err := r.Generate(context.Background(), &model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "bare" || resp.Model != "m9" {
		t.Errorf("provider/model = %q/%q, want bare/m9", resp.Provider, resp.Model)
	}
}
