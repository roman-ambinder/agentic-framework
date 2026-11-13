//go:build live

package harness

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model/anthropic"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// liveSystemPrompt is long enough (over 20 000 characters) to be cached by
// the provider. It is the same text in every step.
func liveSystemPrompt() string {
	const para = "You are a test agent. When the user asks for the ping tool, call it and " +
		"wait for its answer before you say anything else. Keep replies short. "
	return strings.Repeat(para, 250)
}

// TestLiveCacheReadsAcrossSteps runs a short agent against the real API and
// checks that a later step reads the prompt cache: the prefix of each request
// must be the same as the previous one (tools, system, earlier messages).
func TestLiveCacheReadsAcrossSteps(t *testing.T) {
	modelID := os.Getenv("ADLC_ANTHROPIC_MODEL")
	if os.Getenv("ANTHROPIC_API_KEY") == "" || modelID == "" {
		t.Skip("set ANTHROPIC_API_KEY and ADLC_ANTHROPIC_MODEL")
	}
	system := liveSystemPrompt()
	if len(system) < 20000 {
		t.Fatalf("system prompt has %d characters, want at least 20000", len(system))
	}
	ping := funcTool("live__ping", adlcv1.RiskClass_RISK_CLASS_READ,
		func(context.Context, json.RawMessage) (toolrouter.Result, error) {
			return toolrouter.Result{Output: []byte("pong")}, nil
		})
	router, err := toolrouter.NewStatic(ping)
	if err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig()
	cfg.RunID = "live-cache"
	cfg.AgentID = "live-cache@1"
	cfg.AgentVersion = "live-cache@1"
	cfg.Task = "Call the ping tool, then call it again, then reply with the word done."
	cfg.System = system
	cfg.Model = modelID
	store := newLog(t)
	deps := Deps{Log: store, Model: anthropic.New(anthropic.Options{}), Tools: router, Policy: policy.AllowAll{}}
	if _, err := Run(context.Background(), cfg, deps); err != nil {
		t.Fatal(err)
	}
	events, err := store.Read(context.Background(), cfg.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var costs []*adlcv1.Cost
	for _, ev := range events {
		if ev.GetModelCallCompleted() != nil {
			costs = append(costs, ev.GetCost())
		}
	}
	if len(costs) < 2 {
		t.Fatalf("%d model calls, want at least 2", len(costs))
	}
	hit := false
	for i, c := range costs {
		t.Logf("step %d: input %d, cache read %d, cache write %d", i+1, c.GetInputTokens(), c.GetCacheReadTokens(), c.GetCacheWriteTokens())
		if i >= 1 && i <= 2 && c.GetCacheReadTokens() > 0 {
			hit = true
		}
	}
	if !hit {
		t.Error("no cache read in step 2 or 3: the request prefix changed between steps")
	}
}
