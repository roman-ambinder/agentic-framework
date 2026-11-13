package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrScriptExhausted is wrapped by the error Fake.Generate returns when the
// script has no turns left.
var ErrScriptExhausted = errors.New("model: fake script exhausted")

// FakeTurn is one scripted answer. Exactly one of Response and Err is set.
type FakeTurn struct {
	Response *Response
	Err      error
}

// Fake returns its script one turn per Generate call and records requests.
type Fake struct {
	mu       sync.Mutex
	turns    []FakeTurn
	next     int
	requests []*Request
}

// NewFake returns a Fake that answers with turns in order.
func NewFake(turns ...FakeTurn) *Fake {
	return &Fake{turns: append([]FakeTurn(nil), turns...)}
}

// Provider returns "fake".
func (f *Fake) Provider() string { return "fake" }

// Capabilities reports tool calling only.
func (f *Fake) Capabilities() Caps { return Caps{ToolCalling: true} }

// Generate records a copy of req and returns the next scripted turn.
func (f *Fake) Generate(ctx context.Context, req *Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, copyRequest(req))
	if f.next == len(f.turns) {
		return nil, fmt.Errorf("%w after %d turns", ErrScriptExhausted, len(f.turns))
	}
	turn := f.turns[f.next]
	f.next++
	if turn.Err != nil {
		return nil, turn.Err
	}
	return copyResponse(turn.Response), nil
}

// CountTokens estimates ceil(bytes/4) over the request's text content. It
// consumes no turn.
func (f *Fake) CountTokens(ctx context.Context, req *Request) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	total := int64(len(req.System))
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			total += int64(len(p.Text))
			if p.ToolCall != nil {
				total += int64(len(p.ToolCall.Arguments))
			}
			if p.ToolResult != nil {
				total += int64(len(p.ToolResult.Content))
			}
		}
	}
	for _, t := range req.Tools {
		total += int64(len(t.Name) + len(t.Description) + len(t.InputSchema))
	}
	return (total + 3) / 4, nil
}

// Requests returns copies of every request received, in order.
func (f *Fake) Requests() []*Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*Request(nil), f.requests...)
}

// Remaining returns the number of turns not yet used.
func (f *Fake) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.turns) - f.next
}

func copyRequest(r *Request) *Request {
	if r == nil {
		return nil
	}
	c := *r
	c.Messages = copyMessages(r.Messages)
	if r.Tools != nil {
		c.Tools = make([]ToolSpec, len(r.Tools))
		for i, t := range r.Tools {
			t.InputSchema = copyBytes(t.InputSchema)
			c.Tools[i] = t
		}
	}
	return &c
}

func copyResponse(r *Response) *Response {
	if r == nil {
		return nil
	}
	c := *r
	c.Message = copyMessage(r.Message)
	return &c
}

func copyMessages(ms []Message) []Message {
	if ms == nil {
		return nil
	}
	out := make([]Message, len(ms))
	for i, m := range ms {
		out[i] = copyMessage(m)
	}
	return out
}

func copyMessage(m Message) Message {
	if m.Parts == nil {
		return m
	}
	parts := make([]Part, len(m.Parts))
	for i, p := range m.Parts {
		if p.ToolCall != nil {
			tc := *p.ToolCall
			tc.Arguments = copyBytes(tc.Arguments)
			p.ToolCall = &tc
		}
		if p.ToolResult != nil {
			tr := *p.ToolResult
			p.ToolResult = &tr
		}
		if p.Blob != nil {
			b := *p.Blob
			b.Data = copyBytes(b.Data)
			p.Blob = &b
		}
		parts[i] = p
	}
	m.Parts = parts
	return m
}

func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte{}, b...)
}
