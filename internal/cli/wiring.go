package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/model/anthropic"
	"github.com/roman-ambinder/agentic-framework/internal/model/openai"
	modelrouter "github.com/roman-ambinder/agentic-framework/internal/model/router"
	"github.com/roman-ambinder/agentic-framework/internal/plugin"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter/mcprouter"
)

// hookProcess is a started hook plugin; *plugin.Process implements it.
type hookProcess interface {
	harness.Hook
	Close() error
}

// hookStarter starts one hook plugin.
type hookStarter func(ctx context.Context, cfg plugin.Config) (hookProcess, error)

// startPluginHook is the default hookStarter.
func startPluginHook(ctx context.Context, cfg plugin.Config) (hookProcess, error) {
	p, err := plugin.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// pluginConfig maps a declared hook to a plugin config.
func pluginConfig(h agentconfig.ParsedHook) (plugin.Config, error) {
	cmd, err := resolveCommand(h.Command)
	if err != nil {
		return plugin.Config{}, err
	}
	env := make([]string, 0, len(h.Env))
	for k, v := range h.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return plugin.Config{
		Name: h.Name, Command: cmd, Env: env, Dir: h.Dir,
		StartTimeout: h.StartTimeout, CallTimeout: h.CallTimeout,
	}, nil
}

// startHooks starts the agent's hooks in declared order. On failure it closes
// the ones already started. closeAll closes the started hooks in reverse order.
func startHooks(ctx context.Context, a *agentconfig.Agent, start hookStarter) (hooks []harness.Hook, closeAll func() error, err error) {
	if start == nil {
		start = startPluginHook
	}
	var started []hookProcess
	closeAll = func() error {
		var errs []error
		for i := len(started) - 1; i >= 0; i-- {
			errs = append(errs, started[i].Close())
		}
		started = nil
		return errors.Join(errs...)
	}
	for _, h := range a.HookPlugins() {
		cfg, err := pluginConfig(h)
		if err == nil {
			var p hookProcess
			if p, err = start(ctx, cfg); err == nil {
				started = append(started, p)
				hooks = append(hooks, p)
				continue
			}
		}
		_ = closeAll()
		return nil, nil, fmt.Errorf("run: hook %s: %w", h.Name, err)
	}
	return hooks, closeAll, nil
}

// buildRun builds the harness config and dependencies for agent a.
// cleanup closes the hooks and the tool router; call it once after the run
// (safe if nil error). On error everything started has already been closed.
func buildRun(ctx context.Context, a *agentconfig.Agent, o runOptions, runID string, log harness.Log) (cfg harness.Config, deps harness.Deps, cleanup func() error, err error) {
	m, err := buildModel(a, o.fakeScript)
	if err != nil {
		return cfg, deps, nil, err
	}
	pm, err := buildPlanModel(a, o.fakeScript)
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
	hooks, closeHooks, err := startHooks(ctx, a, o.startHook)
	if err != nil {
		_ = router.Close()
		return cfg, deps, nil, err
	}
	cfg = harnessConfig(a, runID, o.task)
	cfg.Hooks = hooks
	deps = harness.Deps{Log: log, Model: m, PlanModel: pm, Tools: router, Policy: a.PolicyEngine()}
	return cfg, deps, func() error { return errors.Join(closeHooks(), router.Close()) }, nil
}

// buildModel picks the adapter from a.Model.Provider. With fallbacks it
// returns a router over the primary and the fallbacks, in config order.
func buildModel(a *agentconfig.Agent, fakeScript string) (model.Adapter, error) {
	primary, err := buildAdapter(a.Model.Provider, fakeScript)
	if err != nil {
		return nil, err
	}
	if len(a.Model.Fallbacks) == 0 {
		return primary, nil
	}
	cands := []modelrouter.Candidate{{Adapter: primary, Model: a.Model.Name}}
	for _, f := range a.Model.Fallbacks {
		ad, err := buildAdapter(f.Provider, fakeScript)
		if err != nil {
			return nil, err
		}
		cands = append(cands, modelrouter.Candidate{Adapter: ad, Model: f.Name})
	}
	r, err := modelrouter.New(cands...)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// buildPlanModel returns the adapter for model.roles.plan, or nil when the
// agent has no plan role (the plan call then uses the primary model).
func buildPlanModel(a *agentconfig.Agent, fakeScript string) (model.Adapter, error) {
	r := a.Model.Roles.Plan
	if r == nil {
		return nil, nil
	}
	return buildAdapter(r.Provider, fakeScript)
}

// buildAdapter returns the adapter for one provider name.
func buildAdapter(provider, fakeScript string) (model.Adapter, error) {
	switch provider {
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
		if fakeScript != "" {
			return nil, errors.New("run: --fake-script only works with provider fake")
		}
		return openai.New(openai.Config{})
	default:
		return nil, fmt.Errorf("run: unsupported provider %q", provider)
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
	cfg := harness.Config{
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
		Prices:             a.Prices(),
		MaxToolOutputBytes: a.MaxToolOutputBytes,
		Context:            a.ContextBudget(),
		Plan:               harness.PlanConfig{Enabled: a.Planning.Enabled, Prompt: a.Planning.Prompt, MaxTokens: a.Planning.MaxTokens},
	}
	if r := a.Model.Roles.Plan; r != nil {
		cfg.Plan.Model, cfg.Plan.Effort = r.Name, r.Effort
	}
	return cfg
}
