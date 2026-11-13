package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/model/anthropic"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter/mcprouter"
)

// buildRun builds the harness config and dependencies for agent a.
// cleanup closes the tool router; call it once after the run (safe if nil error).
func buildRun(ctx context.Context, a *agentconfig.Agent, o runOptions, runID string, log harness.Log) (cfg harness.Config, deps harness.Deps, cleanup func() error, err error) {
	m, err := buildModel(a, o.fakeScript)
	if err != nil {
		return cfg, deps, nil, err
	}
	servers, err := toServerConfigs(a.ToolServers())
	if err != nil {
		return cfg, deps, nil, err
	}
	var router toolrouter.Router
	if len(servers) == 0 {
		router, err = toolrouter.NewStatic()
		if err != nil {
			return cfg, deps, nil, fmt.Errorf("run: start tools: %w", err)
		}
	} else {
		router, err = mcprouter.Start(ctx, servers)
		if err != nil {
			return cfg, deps, nil, fmt.Errorf("run: start tools: %w", err)
		}
	}
	cfg = harnessConfig(a, runID, o.task)
	deps = harness.Deps{Log: log, Model: m, Tools: router, Policy: a.PolicyEngine()}
	return cfg, deps, router.Close, nil
}

// buildModel picks the adapter from a.Model.Provider.
func buildModel(a *agentconfig.Agent, fakeScript string) (model.Adapter, error) {
	switch p := a.Model.Provider; p {
	case "fake":
		if fakeScript == "" {
			return nil, errors.New("run: provider fake needs --fake-script")
		}
		turns, err := model.LoadFakeScript(fakeScript)
		if err != nil {
			return nil, err
		}
		return model.NewFake(turns...), nil
	case "anthropic":
		if fakeScript != "" {
			return nil, errors.New("run: --fake-script only works with provider fake")
		}
		return anthropic.New(anthropic.Options{}), nil
	case "openai":
		return nil, errors.New("run: provider openai is not wired yet (task 1.2)")
	default:
		return nil, fmt.Errorf("run: unsupported provider %q", p)
	}
}

// resolveCommand replaces a leading "adlc" with this executable's path, so
// built-in tool servers run from the same binary.
func resolveCommand(cmd []string) ([]string, error) {
	out := append([]string(nil), cmd...)
	if len(out) == 0 || out[0] != "adlc" {
		return out, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("run: find own executable: %w", err)
	}
	out[0] = exe
	return out, nil
}

// toServerConfigs converts parsed tool servers to router configs.
func toServerConfigs(in []agentconfig.ParsedToolServer) ([]mcprouter.ServerConfig, error) {
	out := make([]mcprouter.ServerConfig, 0, len(in))
	for _, s := range in {
		cmd, err := resolveCommand(s.Command)
		if err != nil {
			return nil, err
		}
		out = append(out, mcprouter.ServerConfig{
			Name: s.Name, Command: cmd, Env: s.Env, Dir: s.Dir,
			Risk: s.Risk, DefaultRisk: s.DefaultRisk,
		})
	}
	return out, nil
}

// harnessConfig maps an agent definition to a harness config.
func harnessConfig(a *agentconfig.Agent, runID, task string) harness.Config {
	return harness.Config{
		RunID:              runID,
		AgentID:            a.AgentID(),
		AgentVersion:       a.AgentID(),
		Task:               task,
		System:             a.System,
		Model:              a.Model.Name,
		MaxTokens:          a.Model.MaxTokens,
		Effort:             a.Model.Effort,
		Budget:             a.BudgetProto(),
		Price:              a.Price(),
		MaxToolOutputBytes: a.MaxToolOutputBytes,
	}
}
