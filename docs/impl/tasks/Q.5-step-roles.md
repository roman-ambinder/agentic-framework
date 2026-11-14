# Q.5 — Model per step role

Size: S · Depends on: Q.4 · Branch: `task/Q.5-step-roles` · PR title: `Q.5: a separate model for the planning call`

## Goal

Each model call has a *role*: `plan` (the planning call of task Q.4) or `act`
(every normal step). After this task an agent file can give the `plan` role its
own model under `model.roles.plan` (provider, name, effort, pricing). `act`
always uses the primary model and its fallbacks, as today. A role without an
entry uses the primary model. The harness calls the plan adapter for the plan
call and prices it with the plan model's price. The trace and `adlc cost --by model`
then show which model served the plan. With no `roles:` section nothing changes
(m2.md decision 6: off by default, switchable per agent file).

## Read first

- `AGENTS.md`, `docs/impl/m2.md`, `docs/impl/tasks/Q.4-plan-step.md` (merged), `docs/impl/interfaces.md` §6, §7
- `internal/agentconfig/agentconfig.go` (`Model`, `resolve`, `expandModel`), `validate.go`
  (`validateModel`, `validateModelFields`, `validateTuning`, `validateFallbackTuning`), `convert.go` (`Prices`, `priceOf`)
- `internal/agentconfig/agentconfig_test.go`: `fallbackYAML`, `TestFallbacksConfig`, `TestFallbackPrices` (copy their style)
- `internal/harness/harness.go`, `loop.go` (`callModel`, `attemptModel`, `priceFor`, `recordResponse`), `plan.go` (from Q.4)
- `internal/model/router/router.go` (a router overwrites `Request.Model`), `internal/model/fake.go`
- `internal/cli/wiring.go` (`buildRun`, `buildModel`, `buildAdapter`, `harnessConfig`), `internal/cli/eval.go` (`evalRunAgent`)
- `internal/costreport/costreport.go` (`Build`, `ByModel`), `internal/observability/span.go`, `tree.go`

## Files

| File | Change |
|------|--------|
| `internal/agentconfig/agentconfig.go` | `Roles` type, `Model.Roles`, expand `${VAR}` in the role |
| `internal/agentconfig/validate.go` | role validation; `modelKind` instead of the `fallback bool` |
| `internal/agentconfig/convert.go` | `Prices` includes the role |
| `internal/agentconfig/agentconfig_test.go` | role tests |
| `internal/agentconfig/testdata/full.yaml` | commented `roles:` example |
| `internal/harness/harness.go` | `PlanConfig.Model`, `PlanConfig.Effort`, `Deps.PlanModel`, validation |
| `internal/harness/loop.go` | `callModel` / `attemptModel` take the adapter |
| `internal/harness/plan.go` | plan call uses the plan adapter, model and effort |
| `internal/harness/roles_test.go` | new |
| `internal/cli/wiring.go` | `buildPlanModel`; `buildRun` and `harnessConfig` use it |
| `internal/cli/eval.go` | `evalRunAgent` passes `PlanModel` |
| `internal/cli/wiring_roles_test.go` | new |
| `docs/impl/interfaces.md` | §6, §7 (text below) |

No change to `events.proto`: `model_call_completed.provider` (8) and `.model` (9) already exist.

## Design

### Agent file

```yaml
name: coder
version: 3
model:
  provider: anthropic
  name: ${ADLC_ANTHROPIC_MODEL}
  max_tokens: 8192
  effort: high
  pricing:                        # ${VAR} only in block style: "{" "}" end a plain scalar in flow style
    input_per_mtok: ${ADLC_ANTHROPIC_PRICE_IN}
    output_per_mtok: ${ADLC_ANTHROPIC_PRICE_OUT}
  roles:                          # optional (task Q.5): a model per step role
    plan:                         # the planning call (task Q.4); "act" is always the primary
      provider: openai            # required; fake, anthropic, openai
      name: ${ADLC_OPENAI_MODEL}  # required; may be exactly ${VAR}
      effort: low                 # optional; "" = the adapter's default (not the primary's effort)
      pricing:                    # optional; same fields and ${VAR} rules as model.pricing
        input_per_mtok: ${ADLC_OPENAI_PRICE_IN}
        output_per_mtok: ${ADLC_OPENAI_PRICE_OUT}
planning:
  enabled: true
policy: {default: deny}
```

`model.roles.plan` is ignored while `planning.enabled` is false, so one file can
switch planning on and off with one line. Not allowed inside a role:
`max_tokens` (the plan call uses `planning.max_tokens`), `fallbacks`, `roles`.
An `act:` key is an unknown field (the strict decoder rejects it).

### `internal/agentconfig`

```go
// Roles holds a model per step role (task Q.5). Role "act" (every normal step)
// always uses the primary model and its fallbacks. A nil entry means the role
// uses the primary model.
type Roles struct {
	Plan *Model `yaml:"plan"` // the planning call (task Q.4)
}
```

`Model` gets `Roles Roles \`yaml:"roles"\`` after `Fallbacks`, with the doc
comment `// Roles is only allowed on the primary model (task Q.5).`

`resolve`: after the fallback loop,
`if r := a.Model.Roles.Plan; r != nil { expandModel(&p, "model.roles.plan", r) }`.
That gives the exact errors of fallbacks, e.g.
`model.roles.plan.name: environment variable X is not set`.

`validate.go`, numbered:

1. Replace `validateModelFields(p, path, m, fallback bool)` by
   `validateModelFields(p *problems, path string, m Model, kind modelKind)` with
   `type modelKind int` and constants `primaryModel`, `fallbackModel`, `roleModel`.
   Inside, `switch kind`: `primaryModel` → `validateTuning`, `fallbackModel` →
   `validateFallbackTuning`, `roleModel` → `validateRoleTuning`. Update both old calls.
2. Move the effort `switch` out of `validateTuning` into
   `validateEffort(p *problems, path, effort string)` (same message:
   `must be one of low, medium, high, xhigh, max, got %q`); `validateTuning` calls it.
3. `validateRoleTuning(p, path, m)`: if `m.MaxTokens != 0` →
   `p.add(path+".max_tokens", "not supported on roles (planning.max_tokens is used)")`;
   then `validateEffort(p, path+".effort", m.Effort)`.
4. In `validateModel`, inside the fallback loop: `if f.Roles.Plan != nil { p.add(path+".roles", "not allowed") }`.
5. At the end of `validateModel`: if `r := a.Model.Roles.Plan; r != nil`:
   `validateModelFields(p, "model.roles.plan", *r, roleModel)`; `len(r.Fallbacks) > 0` →
   `p.add("model.roles.plan.fallbacks", "not allowed")`; `r.Roles.Plan != nil` →
   `p.add("model.roles.plan.roles", "not allowed")`.
   A role may repeat the primary's provider and name (that is allowed and harmless).

`Prices()`: after the fallback loop, if the role exists and its `Name` is not
in `out` yet, `out[r.Name] = priceOf(r.Pricing)`. Order of precedence for a
shared name: primary, then fallbacks, then the role (update the doc comment).

### `internal/harness`

```go
type PlanConfig struct {
	Enabled   bool
	Prompt    string
	MaxTokens int
	// Model is the model id of the plan call (task Q.5); "" = Config.Model,
	// and then Config.Effort is used as well.
	Model string
	// Effort of the plan call; used only when Model is set ("" = adapter default).
	Effort string
}

// in Deps, after Model:
	// PlanModel is the adapter for the plan call (task Q.5); nil = Model.
	PlanModel model.Adapter
```

`validate` adds: `deps.PlanModel != nil && cfg.Plan.Model == ""` →
`errors.New("harness: config: plan model is empty but deps.PlanModel is set")`.
`cfg.Plan.Model != ""` with `deps.PlanModel == nil` is valid: `Deps.Model` is
asked for that model id (fine for a plain adapter; a router ignores it, see Pitfalls).

Loop changes, numbered:

1. `callModel(ad model.Adapter, req *model.Request, callID string, step uint32)` and
   `attemptModel(ad model.Adapter, req, callID, step, attempt)`. In `attemptModel`,
   `model_call_started` gets `Provider: ad.Provider(), Model: req.Model` and the call is
   `ad.Generate(l.callCtx, req)`. (For normal steps `req.Model == l.cfg.Model`, so
   nothing changes for them.)
2. `step` calls `l.callModel(l.deps.Model, req, callID, step)`.
3. In `plan.go` add
   `func (l *loop) planAdapter() model.Adapter` (`l.deps.PlanModel` if non-nil, else `l.deps.Model`) and
   `func (l *loop) planModel() (id, effort string)` (`l.cfg.Plan.Model, l.cfg.Plan.Effort` if
   `l.cfg.Plan.Model != ""`, else `l.cfg.Model, l.cfg.Effort`).
   `planRequest` sets `Model` and `Effort` from `planModel()`; `runPlan` calls
   `l.callModel(l.planAdapter(), req, PlanCallID, 0)`.
4. Pricing needs no new code: `recordResponse` already prices with
   `l.priceFor(resp.Model)` and writes `resp.Provider` / `resp.Model` into
   `model_call_completed` fields 8 and 9. `Config.Prices` must contain the plan
   model's id (`harnessConfig` gets it from `a.Prices()`).
5. Budgets need no new code: the plan's cost is on its event (Q.4).
6. `Resume` needs no new code: it uses the same `runPlan`, so pass the same
   `Deps.PlanModel` to `Resume` as to `Run`.

### `internal/cli`

```go
// buildPlanModel returns the adapter for model.roles.plan, or nil when the
// agent has no plan role (the plan call then uses the primary model).
func buildPlanModel(a *agentconfig.Agent, fakeScript string) (model.Adapter, error) {
	r := a.Model.Roles.Plan
	if r == nil {
		return nil, nil
	}
	return buildAdapter(r.Provider, fakeScript)
}
```

- `buildRun`: after `buildModel`, `pm, err := buildPlanModel(a, o.fakeScript)` (return the error
  as is), and `deps = harness.Deps{Log: log, Model: m, PlanModel: pm, Tools: router, Policy: a.PolicyEngine()}`.
- `harnessConfig`: after building `cfg`, `if r := a.Model.Roles.Plan; r != nil { cfg.Plan.Model, cfg.Plan.Effort = r.Name, r.Effort }`
  (turn the `return harness.Config{...}` into `cfg := ...; ...; return cfg`).
- `evalRunAgent`: after `buildModel`, `pm, err := buildPlanModel(a, "")`, and add `PlanModel: pm` to its `harness.Deps`.

The plan adapter is always a separate adapter, never the primary's router, so
`model_call_started` of the plan names the plan provider and model. That is what
`costreport` (`ByModel` keys rows by `model_call_started` provider/model) and the
trace tree (`model plan  …  <provider>/<model>`) show.

### `interfaces.md` text to add

§6, in `Deps` after `Model`: `PlanModel model.Adapter // task Q.5: adapter of the plan call; nil = Model`;
in `PlanConfig`: the `Model` and `Effort` fields above; after §6.1's plan line: `With Deps.PlanModel
(task Q.5) the plan's model_call_started has provider PlanModel.Provider() and model Plan.Model; its cost
uses Prices[response model].`
§7: `Task Q.5 adds model.roles.plan (provider, name, effort, pricing; ${VAR} as for fallbacks; max_tokens,
fallbacks and roles are rejected; ignored while planning is disabled). Prices() includes it (primary, then
fallbacks, then the role; the first price for a name wins). Wiring: buildPlanModel(a, fakeScript)
(model.Adapter, error), nil without a role.`

## Steps

1. Branch from the latest `main` (Q.4 merged).
2. `agentconfig`: refactor to `modelKind` and `validateEffort`; existing tests still pass.
3. Write the role tests; add `Roles`, expansion, validation, `Prices`; pass.
4. `harness`: add the fields and validation; change `callModel`/`attemptModel`; existing tests pass.
5. Write `roles_test.go`; change `plan.go`; pass.
6. `cli`: `buildPlanModel`, `buildRun`, `harnessConfig`, `evalRunAgent`; write `wiring_roles_test.go`; pass.
7. `full.yaml` comment block; `interfaces.md`.
8. Break the code on purpose (for example call `l.deps.Model` in `runPlan`), see the tests fail, restore.
9. `make fmt && make ci`.

## Tests

Harness tests use two fakes. Add to `roles_test.go`:

```go
// namedAdapter is a Fake that reports another provider name, so two fakes can be told apart.
type namedAdapter struct {
	*model.Fake
	provider string
}

func (n namedAdapter) Provider() string { return n.provider }

func (n namedAdapter) Generate(ctx context.Context, req *model.Request) (*model.Response, error) {
	resp, err := n.Fake.Generate(ctx, req)
	if resp != nil {
		resp.Provider = n.provider
	}
	return resp, err
}
```

Common setup: `cfg := baseConfig(); cfg.Effort = "high"; cfg.Plan = PlanConfig{Enabled: true, Model: "plan-m", Effort: "low"}`;
`act := model.NewFake(textTurn("ok"))`; `planner := namedAdapter{model.NewFake(textTurn("the plan")), "planner"}`;
`Run` with `Deps{Log: store, Model: act, PlanModel: planner, Tools: staticTools(t), Policy: policy.AllowAll{}}`, then `assertFolds`. "Event N" means the event with seq N (`events[N-1]`).

| Test | Asserts |
|------|---------|
| `TestPlanRoleUsesPlanAdapter` | planner got 1 request: `Model "plan-m"`, `Effort "low"`, no tools, `MaxTokens 1024`; act got 1 request: `Model "m"`, `Effort "high"`, 1 tool, `Messages[1]` text `the plan`; both scripts fully used (`Remaining() == 0`) |
| `TestPlanRoleRecordsProviderAndModel` | event 2 `model_call_started` {call `plan`, step 0, provider `planner`, model `plan-m`}; event 3 `model_call_completed` provider `planner`, model `plan-m`; event 4 started {`m1`, `fake`, `m`}; event 5 completed {`fake`, `m`} |
| `TestPlanRolePricedByPlanModel` | `cfg.Price = {InputPerMTok: 1}`, `cfg.Prices = {"m": {InputPerMTok: 1_000_000}, "plan-m": {InputPerMTok: 3_000_000}}`, both turns `Usage{InputTokens: 10}`: plan event `usd_micros 30`, m1 event `usd_micros 10`, `state.Usage.USDMicros == 40` |
| `TestPlanRoleCostReport` | same run as above; `costreport.Build(costreport.ByModel, map[string][]*adlcv1.Event{"r1": events})`: rows (sorted) are exactly `fake/m` (ModelCalls 1, USDMicros 10) and `planner/plan-m` (ModelCalls 1, USDMicros 30) |
| `TestPlanRoleTrace` | `observability.Build(events)` then `observability.WriteTree` into a buffer: one line contains both `model plan` and `planner/plan-m`, another both `model m1` and `fake/m` |
| `TestPlanModelWithoutPlanAdapter` | `PlanModel` nil, one fake with `textTurn("the plan"), textTurn("ok")`: request 0 `Model "plan-m"`, `Effort "low"`; request 1 `Model "m"`, `Effort "high"`; event 2 provider `fake` model `plan-m` |
| `TestPlanAdapterWithoutPlanModelRejected` | `cfg.Plan = PlanConfig{Enabled: true}` with `PlanModel` set: `Run` error text is exactly `harness: config: plan model is empty but deps.PlanModel is set`; the log has no events |
| `TestPlanRoleIgnoredWhenPlanningDisabled` | `cfg.Plan.Enabled = false` (Model still `plan-m`), planner set: planner got 0 requests; kinds `run_started, model_call_started, model_call_completed, run_completed` |
| `TestResumeUsesPlanAdapter` | `crashRun(t, 2, cfg, deps, …)` with `deps.PlanModel = namedAdapter{model.NewFake(textTurn("the plan")), "planner"}` (dies before the plan completes); `resumeOK` with a new planner (`textTurn("the plan")`) in `deps.PlanModel` and act turns `textTurn("ok")`: the new planner got 1 request with `Model "plan-m"`; the tail's plan `model_call_completed` has provider `planner`; stop COMPLETED |
| `TestPlanRoleConfig` (agentconfig) | with `planRoleYAML` (below) and `ADLC_TEST_MODEL=primary-1`, `ADLC_TEST_PLAN_MODEL=plan-1`: `Roles.Plan` is `{openai, plan-1, effort low}`; table of `strings.Replace` variants, each error contains: missing name → `model.roles.plan.name: is required`; `${ADLC_TEST_NOPE}` name → `model.roles.plan.name: environment variable ADLC_TEST_NOPE is not set`; provider `nope` → `model.roles.plan.provider: must be one of fake, anthropic, openai, got "nope"`; `      max_tokens: 100` inserted after `      effort: low` → `model.roles.plan.max_tokens: not supported on roles (planning.max_tokens is used)`; effort `huge` → `model.roles.plan.effort: must be one of low, medium, high, xhigh, max, got "huge"`; `      fallbacks: [{provider: openai, name: x}]` inserted after `      effort: low` → `model.roles.plan.fallbacks: not allowed`; `      roles: {plan: {provider: openai, name: x}}` inserted after `      effort: low` → `model.roles.plan.roles: not allowed`; `    plan:` renamed `    act:` → `unknown field "act"`; line `        input_per_mtok: "1"` changed to `        input_per_mtok: x` → `model.roles.plan.pricing.input_per_mtok: "x" is not a decimal amount`; changed to `        input_per_mtok: ${ADLC_TEST_NOPE}` → `model.roles.plan.pricing.input_per_mtok: environment variable ADLC_TEST_NOPE is not set` |
| `TestFallbackRolesRejected` (agentconfig) | `fallbackYAML` with `      roles: {plan: {provider: openai, name: x}}\n` before `      pricing:` → `model.fallbacks[0].roles: not allowed` |
| `TestPlanRolePrices` (agentconfig) | `Prices()` of `planRoleYAML` = `{"primary-1": {3_000_000, 15_000_000}, "plan-1": {1_000_000, 2_000_000}}`; a role named like the primary keeps the primary's price |
| `TestBuildPlanModel` (cli) | no role → `nil, nil`; role `anthropic` → `*anthropic.Adapter`; role `fake` without script → error `run: provider fake needs --fake-script` |
| `TestHarnessConfigPassesPlanRole` (cli) | role `{openai, plan-1, effort low}` → `cfg.Plan.Model == "plan-1"`, `cfg.Plan.Effort == "low"`, `cfg.Prices` has `plan-1`; no role → both `""` |

```go
const planRoleYAML = `name: a
version: 1
model:
  provider: anthropic
  name: ${ADLC_TEST_MODEL}
  pricing: {input_per_mtok: "3", output_per_mtok: "15"}
  roles:
    plan:
      provider: openai
      name: ${ADLC_TEST_PLAN_MODEL}
      effort: low
      pricing:
        input_per_mtok: "1"
        output_per_mtok: "2"
planning: {enabled: true}
policy: {default: deny}
`
```

## Verify

```bash
go test ./internal/agentconfig/ -run 'Role|Fallback' -v
go test ./internal/harness/ -run 'Plan|Resume' -v
go test ./internal/cli/ -run 'PlanModel|PlanRole|Fallback' -v
make ci
```

Expected: every test `PASS`; `make ci` ends with `0 issues.` and no stale `gen/`.

## Out of scope

- Roles other than `plan`; a model per tool or per difficulty; choosing a model at run time.
- Fallbacks for the plan role (a role has exactly one model).
- Changing `costreport` or `observability` (both already read the plan's `model_call_started`).
- Prompt caching across the two models (the plan call has no tools, so it never shares a cache prefix).

## Pitfalls

- Never reuse the primary adapter for a role when the primary is a router: `router.Generate`
  replaces `Request.Model` with each candidate's model, so the plan model id would be ignored.
  `buildPlanModel` always builds a new adapter with `buildAdapter`.
- `buildPlanModel` must return a plain `nil` interface, never a typed nil pointer; otherwise
  `Deps.PlanModel != nil` is true and the harness calls a nil adapter.
- `model_call_started.model` must come from `req.Model`, not `l.cfg.Model`, or the plan is
  recorded under the act model.
- An empty role `effort` means the adapter default, not the primary's effort: the primary's
  effort may be invalid for another provider.
- With `provider: fake` in both the primary and the role, `adlc run --fake-script` loads the
  same script twice, into two independent fakes: both start at the first turn.
- Do not add `max_tokens` handling to roles: the plan call's limit is `planning.max_tokens`.
- Keep the existing fallback error texts unchanged; tests check them.

## Done checklist

- [ ] `model.roles.plan` parsed, `${VAR}`-expanded, validated, priced, documented in `full.yaml`
- [ ] `Deps.PlanModel`, `PlanConfig.Model/Effort`, validation error
- [ ] Plan call uses the plan adapter, model and effort; act unchanged
- [ ] Events, cost, cost report and trace name the plan model
- [ ] `adlc run`, `adlc resume` and `adlc eval` pass the plan adapter
- [ ] `interfaces.md` §6, §7 updated
- [ ] `make ci` passes
