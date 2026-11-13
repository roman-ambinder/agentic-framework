// Package contextmgr keeps a model request inside a token budget. When the
// estimated size is over budget it replaces old tool outputs with a short
// placeholder. It never removes a message or a part, so the conversation stays
// valid for every provider. The event log is not touched; only the request
// sent to the model is compacted.
package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/roman-ambinder/agentic-framework/internal/model"
)

// defaultKeepRecentSteps is Budget.KeepRecentSteps when it is zero.
const defaultKeepRecentSteps = 2

// defaultLowWaterRatio is Budget.LowWaterRatio when it is zero.
const defaultLowWaterRatio = 0.7

// elidedPrefix starts every placeholder; a result that starts with it is
// never elided again.
const elidedPrefix = "[tool output elided:"

// Budget limits the size of one model request.
type Budget struct {
	// MaxInputTokens is the budget for system + tools + messages.
	// 0 means no limit: Build returns the request unchanged.
	MaxInputTokens int64
	// KeepRecentSteps is how many of the latest assistant messages (and
	// the tool results answering them) are never elided. 0 means 2.
	KeepRecentSteps int
	// LowWaterRatio: when eliding, keep going until the estimate is at or
	// below MaxInputTokens * LowWaterRatio. 0 means 0.7. Must be in (0, 1].
	LowWaterRatio float64
	// Count, if set, gives an exact token count. It is called only when
	// the estimate is within 10% of MaxInputTokens.
	Count func(ctx context.Context, req *model.Request) (int64, error)
}

// Stats describes what Build did.
type Stats struct {
	EstimatedTokens int64 // estimate of the returned request
	ElidedResults   int   // tool results replaced by a placeholder
}

// ErrContextTooLarge means the request does not fit even after eliding
// every tool result outside the protected window.
var ErrContextTooLarge = errors.New("contextmgr: context budget exceeded")

// Build returns a copy of req whose messages fit the budget. req itself is
// never modified. When the request cannot be made to fit, the compacted copy
// is returned together with an error wrapping ErrContextTooLarge.
func Build(ctx context.Context, req *model.Request, b Budget) (*model.Request, Stats, error) {
	out, st, _, err := build(ctx, req, b, nil)
	return out, st, err
}

// build is Build with the elisions of earlier steps. Every tool result at a
// location in elided is replaced by its placeholder first, without being
// counted in Stats. It returns the locations elided by this call, oldest first.
func build(ctx context.Context, req *model.Request, b Budget, elided map[location]bool) (*model.Request, Stats, []location, error) {
	ratio, err := lowWaterRatio(b)
	if err != nil {
		return nil, Stats{}, nil, err
	}
	out := deepCopy(req)
	applyElisions(out.Messages, elided)
	est := EstimateRequest(out)
	if b.MaxInputTokens == 0 {
		return out, Stats{EstimatedTokens: est}, nil, nil
	}
	if fit, _, _ := fits(ctx, out, est, b); fit {
		return out, Stats{EstimatedTokens: est}, nil, nil
	}
	target := int64(float64(b.MaxInputTokens) * ratio)
	var newly []location
	for _, loc := range elidable(out.Messages, protectedFrom(out.Messages, keepSteps(b))) {
		tr := out.Messages[loc.msg].Parts[loc.part].ToolResult
		est += elideResult(tr)
		newly = append(newly, loc)
		if est > target {
			continue
		}
		if fit, _, _ := fits(ctx, out, est, b); fit {
			return out, Stats{EstimatedTokens: est, ElidedResults: len(newly)}, newly, nil
		}
	}
	stats := Stats{EstimatedTokens: est, ElidedResults: len(newly)}
	fit, n, counted := fits(ctx, out, est, b)
	if fit {
		return out, stats, newly, nil
	}
	if !counted {
		n = est
	}
	return out, stats, newly, fmt.Errorf("%w: needs about %d tokens, budget is %d", ErrContextTooLarge, n, b.MaxInputTokens)
}

// lowWaterRatio returns the effective LowWaterRatio, or an error when it is
// outside (0, 1].
func lowWaterRatio(b Budget) (float64, error) {
	if err := b.Validate(); err != nil {
		return 0, err
	}
	if b.LowWaterRatio == 0 {
		return defaultLowWaterRatio, nil
	}
	return b.LowWaterRatio, nil
}

// Validate reports whether the budget is usable: LowWaterRatio must be 0
// (default) or in (0, 1]. Build applies the same check.
func (b Budget) Validate() error {
	r := b.LowWaterRatio
	if r != 0 && !(r > 0 && r <= 1) {
		return fmt.Errorf("contextmgr: LowWaterRatio %v must be in (0, 1]", r)
	}
	return nil
}

func keepSteps(b Budget) int {
	if b.KeepRecentSteps == 0 {
		return defaultKeepRecentSteps
	}
	return b.KeepRecentSteps
}

// fits reports whether r fits the budget. n is the exact count when Count was
// called and counted is true.
func fits(ctx context.Context, r *model.Request, est int64, b Budget) (fit bool, n int64, counted bool) {
	if est > b.MaxInputTokens {
		return false, 0, false
	}
	if b.Count == nil || est < b.MaxInputTokens*9/10 {
		return true, 0, false
	}
	n, err := b.Count(ctx, r)
	if err != nil {
		return true, 0, false // the estimate fits; a count error does not fail the run
	}
	return n <= b.MaxInputTokens, n, true
}

// protectedFrom returns the index of the keep-th assistant message counted
// from the end, or 0 when there are fewer than keep assistant messages.
func protectedFrom(msgs []model.Message, keep int) int {
	seen := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != model.RoleAssistant {
			continue
		}
		if seen++; seen == keep {
			return i
		}
	}
	return 0
}

// location is the position of a part in the request: message index, then part
// index. Messages are only appended, so a location names the same part on
// every later step.
type location struct{ msg, part int }

// elidable lists, oldest first, the tool results that may still be elided:
// messages 1 up to (not including) protectedFrom, never message 0.
func elidable(msgs []model.Message, protectedFrom int) []location {
	var out []location
	for i := 1; i < protectedFrom; i++ {
		if msgs[i].Role != model.RoleUser {
			continue
		}
		for j, p := range msgs[i].Parts {
			if p.Kind == model.PartToolResult && p.ToolResult != nil && worthEliding(p.ToolResult.Content) {
				out = append(out, location{i, j})
			}
		}
	}
	return out
}

// elideResult replaces the content of tr with its placeholder and returns the
// change in estimated tokens.
func elideResult(tr *model.ToolResult) int64 {
	placeholder := placeholderFor(tr.Content)
	delta := EstimateTokens(placeholder) - EstimateTokens(tr.Content)
	tr.Content = placeholder
	return delta
}

// applyElisions replaces the content of every tool result at a location in
// elided. It works on a copy made by deepCopy, so the input is not changed.
func applyElisions(msgs []model.Message, elided map[location]bool) {
	if len(elided) == 0 {
		return
	}
	for i := range msgs {
		for j, p := range msgs[i].Parts {
			if p.ToolResult != nil && elided[location{i, j}] && !strings.HasPrefix(p.ToolResult.Content, elidedPrefix) {
				p.ToolResult.Content = placeholderFor(p.ToolResult.Content)
			}
		}
	}
}

// worthEliding reports whether replacing content with its placeholder makes
// the request smaller: not already elided, and longer than the placeholder.
func worthEliding(content string) bool {
	return !strings.HasPrefix(content, elidedPrefix) && len(placeholderFor(content)) < len(content)
}

// placeholderFor depends only on the length of the original content, so every
// step writes the same bytes for the same elided result.
func placeholderFor(content string) string {
	return fmt.Sprintf("[tool output elided: %d bytes; call the tool again if you need it]", len(content))
}

// deepCopy copies the messages, their parts and the tool results, which are
// the only things Build changes. Everything else is shared.
func deepCopy(req *model.Request) *model.Request {
	out := *req
	if req.Messages == nil {
		return &out
	}
	out.Messages = make([]model.Message, len(req.Messages))
	for i, m := range req.Messages {
		var parts []model.Part
		if m.Parts != nil {
			parts = make([]model.Part, len(m.Parts))
			copy(parts, m.Parts)
		}
		for j := range parts {
			if parts[j].ToolResult != nil {
				tr := *parts[j].ToolResult
				parts[j].ToolResult = &tr
			}
		}
		out.Messages[i] = model.Message{Role: m.Role, Parts: parts}
	}
	return &out
}
