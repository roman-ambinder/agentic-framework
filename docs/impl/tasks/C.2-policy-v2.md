# C.2 — Policy v2: argument matchers and policy files

Size: M · Depends on: — · Branch: `task/C.2-policy-v2` · PR title: `C.2: policy argument matchers, tool globs and policy files`

## Goal

Today a policy rule (`internal/policy.Rule`, configured under `policy.rules`
in the agent file) matches only a risk class and an exact tool name. After
this task:

- a rule may match **top-level string fields of the tool call's JSON
  arguments** with exactly one of four matchers: `glob`, `prefix`, `regex`, `in`;
- a rule's `tool` is a **glob** (`fs__write_*`); a name without wildcards still
  matches exactly, so every existing agent file keeps working unchanged;
- the agent's `policy` may `include:` **policy files** (paths relative to the
  file that names them); their rules are evaluated before the agent's own
  rules, first match wins, and `default` comes only from the agent file;
- `adlc policy check AGENT_YAML --tool NAME --args JSON` prints the decision,
  the rule id and the reason, for testing a policy by hand.

No schema change, no new dependency (globs are translated to `regexp`).

## Read first

- `AGENTS.md`; `docs/impl/m2.md`; `docs/impl/interfaces.md` §4, §7
- `internal/policy/static.go`, `static_test.go`, `names.go`
- `internal/agentconfig/agentconfig.go` (`Policy`, `Rule`, `parse`, `resolve`, `resolveSystemFile`), `validate.go` (`problems`, `validatePolicy`), `convert.go` (`PolicyEngine`, `ToolServers`), `agentconfig_test.go` (`wantErr`, `TestValidateTable`), `repo_agents_test.go`
- `internal/cli/run.go` (`loadAgent`), `root.go`, `cost.go` (command shape)

## Files

Create:

```
internal/policy/match.go                     ArgMatcher, CheckGlob, glob/regex compilation
internal/policy/match_test.go
internal/agentconfig/include.go              loading policy files
internal/agentconfig/include_test.go
internal/agentconfig/testdata/policy/agent.yaml
internal/agentconfig/testdata/policy/shared.yaml
internal/agentconfig/testdata/policy/base.yaml
internal/cli/policy.go                       adlc policy check
internal/cli/policy_test.go
```

Change:

```
internal/policy/static.go                    Rule.Args, compiled rules, matching
internal/policy/static_test.go               new tests only; existing ones unchanged
internal/agentconfig/agentconfig.go          Policy.Include, Policy.Included, Rule.Args, ArgMatcher, PolicyFile, IncludedRule
internal/agentconfig/validate.go             shared validateRule, included rules
internal/agentconfig/convert.go              PolicyEngine: included rules first, args
internal/agentconfig/agentconfig_test.go     new rows in TestValidateTable
internal/cli/root.go                         root.AddCommand(newPolicyCmd())
docs/impl/interfaces.md                      §4 and §7 (text below)
```

Do not edit `testdata/full.yaml` or any file under `agents/` or `examples/`.

## Design

### Package `policy` (`match.go`)

```go
// ArgMatcher matches one top-level field of a tool call's JSON arguments.
// Exactly one field is set. The argument must be a JSON string; a missing
// field, a non-string value, or arguments that are not a JSON object never match.
type ArgMatcher struct {
	Glob   string   // whole value; "*" = any run of characters except "/", "**" = any run, "?" = one character except "/"
	Prefix string   // strings.HasPrefix(value, Prefix)
	Regex  string   // RE2; must match the whole value (compiled as ^(?:Regex)$)
	In     []string // value equals one of these
}

// Check reports a matcher that does not set exactly one field, a bad glob or
// a bad regex. Its error has no "policy:" prefix: callers put it after a path.
func (m ArgMatcher) Check() error

// CheckGlob reports a glob this package cannot compile. Its error has no
// "policy:" prefix: callers put it after a path.
func CheckGlob(glob string) error
```

"Set" means `Glob != ""`, `Prefix != ""`, `Regex != ""`, `len(In) > 0`.

Exact error texts:

| Problem | Text |
|---------|------|
| zero or several fields set | `set exactly one of glob, prefix, regex, in` |
| glob contains `[` `]` `{` `}` or `\` | `bad glob %q: %q is not supported (use *, ** or ?)` (glob, then the character as a string), e.g. `bad glob "docs/[a]": "[" is not supported (use *, ** or ?)` |
| regex does not compile | `bad regex %q: %v` with the error of `regexp.Compile(m.Regex)`, e.g. `bad regex "(": error parsing regexp: missing closing ): ` + "`(`" |

Glob to regexp (unexported `globRegexp(glob string) (*regexp.Regexp, error)`):

1. Start a `strings.Builder` with `(?s)^`.
2. Walk the glob byte by byte (`i` from 0):
   - `*` followed by `*` → write `.*`, skip the second `*`;
   - `*` → write `[^/]*`;
   - `?` → write `[^/]`;
   - `[`, `]`, `{`, `}`, `\` → return the "bad glob" error;
   - any other byte → write `regexp.QuoteMeta(string(glob[i]))`.
3. Write `$`, `regexp.MustCompile` the result (it always compiles).

`CheckGlob` calls `globRegexp` and drops the regexp. Examples (all in `TestGlob`):

| glob | value | match |
|------|-------|-------|
| `docs/**` | `docs/a.md`, `docs/a/b.md`, `docs/` | yes |
| `docs/**` | `docs`, `src/docs/a.md` | no |
| `*.go` | `main.go` | yes |
| `*.go` | `cmd/main.go` | no |
| `**.go` | `cmd/main.go` | yes |
| `a?c` | `abc` | yes |
| `a?c` | `a/c`, `ac` | no |
| `a.b` | `axb` | no (`.` is literal) |
| `fs__write_*` | `fs__write_file` | yes |
| `fs__write_*` | `fs__read_file` | no |
| `fs__read_file` | `fs__read_file` / `fs__read_files` | yes / no |

### `Rule` and `Static` (`static.go`)

```go
// Rule matches on risk class, tool name glob and (optionally) arguments.
// All given conditions must hold. First match wins.
type Rule struct {
	ID       string
	Risk     adlcv1.RiskClass       // UNSPECIFIED = any risk
	Tool     string                 // "" = any tool; otherwise a glob (a plain name matches exactly)
	Args     map[string]ArgMatcher  // nil = any arguments; every entry must match
	Decision adlcv1.Decision
}

// Static is a Client over a fixed rule list. Build it with NewStatic.
type Static struct {
	Rules    []Rule
	Default  adlcv1.Decision
	compiled []compiledRule // built by NewStatic, same order as Rules
}
```

`compiledRule{tool *regexp.Regexp /* nil = any */; args []argCheck /* sorted by name */}`,
`argCheck{name string; m ArgMatcher; re *regexp.Regexp /* glob or regex, else nil */}`.

`NewStatic` keeps every existing check and message, and after them, per rule:

1. `r.Tool != ""` and `CheckGlob(r.Tool)` fails → `fmt.Errorf("policy: rule %q: tool: %w", r.ID, err)`.
2. For each arg name in sorted order: `name == ""` → `fmt.Errorf("policy: rule %q: args: empty argument name", r.ID)`;
   `m.Check()` fails → `fmt.Errorf("policy: rule %q: args.%s: %w", r.ID, name, err)`.
3. Compile into `compiledRule`. Copy `Args` maps (and `In` slices) so later
   changes by the caller do not leak in (as `TestNewStaticCopiesRules` does for rules).

`Authorize`, after the existing context and unspecified-risk checks:

1. `args := parseArgs(req.ArgumentsJSON)`: `json.Unmarshal` into
   `map[string]json.RawMessage`; any error → `nil`. A JSON `null` also gives `nil`.
2. For `i, r := range s.Rules`: match when risk matches (as today), and
   `compiled[i].tool == nil || compiled[i].tool.MatchString(req.Tool)`, and every
   `argCheck` matches: `raw, ok := args[name]`; `raw = bytes.TrimSpace(raw)`;
   not ok, or `raw` does not start with `"`, or `json.Unmarshal(raw, &s string)` fails → no match;
   then glob/regex → `re.MatchString(s)`, prefix → `strings.HasPrefix`, in → `slices.Contains`.
3. First match returns the same `Decision` as today (`Reason: "matched rule <id>"`);
   no match → `Default` with rule id `default`, reason `no rule matched` (unchanged).

### Agent file (`agentconfig.go`)

```go
// Policy is the agent's policy: a default decision, included policy files and ordered rules.
type Policy struct {
	Default string   `yaml:"default"`
	Include []string `yaml:"include"` // policy files, relative to the agent file
	Rules   []Rule   `yaml:"rules"`
	// Included holds the rules of the included files in evaluation order,
	// filled by Load and Parse. Not in YAML.
	Included []IncludedRule `yaml:"-"`
}

// Rule is one policy rule; the first match wins.
type Rule struct {
	ID       string                `yaml:"id"`
	Risk     string                `yaml:"risk"`
	Tool     string                `yaml:"tool"`
	Args     map[string]ArgMatcher `yaml:"args"`
	Decision string                `yaml:"decision"`
}

// ArgMatcher is policy.ArgMatcher in YAML: exactly one key.
type ArgMatcher struct {
	Glob   string   `yaml:"glob"`
	Prefix string   `yaml:"prefix"`
	Regex  string   `yaml:"regex"`
	In     []string `yaml:"in"`
}

// PolicyFile is a reusable policy file named by policy.include. It has no
// default: the default decision comes only from the agent file.
type PolicyFile struct {
	Include []string `yaml:"include"` // relative to this file
	Rules   []Rule   `yaml:"rules"`
}

// IncludedRule is a rule read from a policy file.
type IncludedRule struct {
	File  string // the policy file's path as resolved (see include.go)
	Index int    // position in that file's rules
	Rule  Rule
}
```

Policy files are decoded with `yaml.UnmarshalWithOptions(data, &pf, yaml.Strict())`,
like agent files. So an unknown matcher (`{suffix: x}`) is the decoder's
`unknown field "suffix"` error, and `default:` in a policy file is
`unknown field "default"`. Full example:

```yaml
# internal/agentconfig/testdata/policy/agent.yaml
name: inc
version: 1
model: {provider: fake}
tools:
  - server: fs
    command: ["adlc", "tool", "fs", "--root", "./workspace"]
    risk: {read_file: read, list_dir: read, write_file: write_local}
policy:
  default: deny
  include:
    - shared.yaml
  rules:
    - id: reads
      risk: read
      decision: allow
```

```yaml
# internal/agentconfig/testdata/policy/shared.yaml
include:
  - base.yaml
rules:
  - id: docs-writes
    tool: "fs__write_*"
    args:
      path: {glob: "docs/**"}
    decision: allow
  - id: other-writes
    tool: "fs__write_*"
    decision: needs_approval
```

```yaml
# internal/agentconfig/testdata/policy/base.yaml
rules:
  - id: no-keys
    args:
      path: {regex: '.*\.(pem|key)'}
    decision: deny
  - id: tmp-reads
    tool: fs__read_file
    args:
      path: {prefix: "tmp/"}
    decision: allow
  - id: known-files
    args:
      path: {in: [README.md, go.mod]}
    decision: allow
```

Evaluation order: `no-keys`, `tmp-reads`, `known-files` (base, included by
shared), `docs-writes`, `other-writes` (shared), `reads` (agent), then `default: deny`.

### Loading includes (`include.go`)

Called from `resolve()` after `resolveSystemFile`: `a.resolvePolicyIncludes(&p)`.

```go
// includeLoader loads policy files depth first; each file is loaded once.
type includeLoader struct {
	seen  map[string]bool // absolute paths already loaded (or being loaded)
	rules []IncludedRule
}
```

`load(p *problems, baseDir, entry string, stack []string /* absolute paths being loaded, outermost first */)`:

1. `entry == ""` → `p.add("policy.include", "empty path")`; return.
2. `f := entry` if `filepath.IsAbs(entry)`, else `filepath.Join(baseDir, entry)`;
   then `f, err := filepath.Abs(f)` (error → `p.add("policy.include", "%v", err)`; return).
   `f` (absolute, clean) is used for everything below: `stack`, `seen`, messages, `IncludedRule.File`.
3. `f` is in `stack` at index `k` → `p.add("policy.include", "include cycle: %s", strings.Join(append(slices.Clone(stack[k:]), f), " -> "))`; return.
4. `seen[f]` → return (a file included twice, not in a cycle, adds its rules once).
5. `seen[f] = true`; `data, err := os.ReadFile(f)`; error → `p.add("policy.include", "%v", err)`; return.
6. Strict decode into `PolicyFile`; error → `p.add("policy.include", "%s: decode: %s", f, yaml.FormatError(err, false, true))`; return.
7. For each `inc` of the file's `Include`: `load(p, filepath.Dir(f), inc, append(stack, f))`.
8. Append the file's rules: `IncludedRule{File: f, Index: i, Rule: r}`.

`resolvePolicyIncludes` runs `load(p, a.Dir, inc, nil)` for each `a.Policy.Include`
in order and stores `l.rules` in `a.Policy.Included`. With `Parse`, `a.Dir` is
`""`, so relative includes are relative to the working directory (still stored as absolute paths).

Resulting messages (the whole `Load` error is prefixed `agentconfig: <agent path>: `):

```
policy.include: open /abs/dir/missing.yaml: no such file or directory
policy.include: include cycle: /abs/dir/a.yaml -> /abs/dir/b.yaml -> /abs/dir/a.yaml
policy.include: /abs/dir/x.yaml: decode: [1:1] unknown field "default" ...
policy.include: empty path
```

### Validation (`validate.go`)

Extract the per-rule checks of `validatePolicy` into
`validateRule(p *problems, path string, r Rule, seen map[string]bool)` with
the existing messages unchanged, plus:

- `r.Tool != ""` and `policy.CheckGlob(r.Tool)` fails → `p.add(path+".tool", "%v", err)`
- for each arg name, sorted: `""` → `p.add(path+".args", "empty argument name")`;
  `r.Args[name].toPolicy().Check()` fails → `p.add(path+".args."+name, "%v", err)`

`validatePolicy`: check `default` as today; one `seen` map; first every
`a.Policy.Included[i]` with `path = fmt.Sprintf("%s: rules[%d]", ir.File, ir.Index)`,
then the agent's rules with `path = fmt.Sprintf("policy.rules[%d]", i)`. Rule ids
are unique across the included files and the agent file. Examples:

```
policy.rules[0].tool: bad glob "fs__[": "[" is not supported (use *, ** or ?)
policy.rules[0].args.path: bad regex "(": error parsing regexp: missing closing ): `(`
policy.rules[0].args.path: set exactly one of glob, prefix, regex, in
policy.rules[0].id: duplicate rule id "no-keys"          (id already used in an included file)
/abs/dir/shared.yaml: rules[1].args.path: bad glob "docs/{a,b}": "{" is not supported (use *, ** or ?)
```

`convert.go`: `func (m ArgMatcher) toPolicy() policy.ArgMatcher`, `func toPolicyRule(r Rule) policy.Rule`
(the existing conversion plus `Args`; nil map stays nil). `PolicyEngine` builds
`rules` = converted `Included` rules (in order) followed by converted `Rules`;
`Default` from `a.Policy.Default` as today.

### `adlc policy check` (`internal/cli/policy.go`)

```
adlc policy check AGENT_YAML --tool NAME [--args JSON] [--risk NAME] [--model-name NAME]
```

`newPolicyCmd()` returns `policy` (Short `Inspect an agent's policy`) with one
subcommand from `newPolicyCheckCmd()` (`cobra.ExactArgs(1)`, `--tool` required
via `MarkFlagRequired`, `--args` default `{}`).
`policyCheck(ctx context.Context, o policyCheckOptions, stdout io.Writer) error`:

1. `a, err := loadAgent(runOptions{agentPath: o.agentPath, modelName: o.modelName})`; error → exit 1.
2. `args := strings.TrimSpace(o.args)`; not `json.Valid` or not starting with `{` →
   exit 1, `policy check: --args must be a JSON object`.
3. Risk: `o.risk != ""` → `policy.ParseRiskClass(o.risk)`, error → exit 1
   `fmt.Errorf("policy check: --risk: %w", err)`. Otherwise `toolRisk(a, o.tool)`:
   `server, name, ok := strings.Cut(tool, "__")`; find the `a.ToolServers()` entry
   with `Name == server`; `Risk[name]` if present, else its `DefaultRisk`;
   no `__` or no such server → `RISK_CLASS_UNSPECIFIED`.
4. `d, err := a.PolicyEngine().Authorize(ctx, policy.Request{AgentID: a.AgentID(), Tool: o.tool, Risk: risk, ArgumentsJSON: args})`.
5. Print with `fmt.Fprintf(stdout, "%-10s%s\n", label, value)` four lines and exit 0 for every decision:

```
$ adlc policy check internal/agentconfig/testdata/policy/agent.yaml --tool fs__write_file --args '{"path":"docs/a.md"}'
decision: allow
rule:     docs-writes
reason:   matched rule docs-writes
risk:     write_local
```

Values: `policy.DecisionName(d.Decision)`, `d.RuleID`, `d.Reason`, `policy.RiskClassName(risk)`.

### `interfaces.md` additions (exact text)

§4: replace the `Rule` block with the `Rule` of this brief, and add after `NewStatic`:

```go
// ArgMatcher (task C.2): exactly one of Glob, Prefix, Regex, In; matches a
// top-level JSON string argument; missing or non-string never matches.
type ArgMatcher struct {
	Glob   string   // "*" no "/", "**" anything, "?" one char not "/"; [ ] { } \ rejected
	Prefix string
	Regex  string   // whole value: ^(?:Regex)$
	In     []string
}
func (m ArgMatcher) Check() error // error text has no "policy:" prefix
func CheckGlob(glob string) error // same
```

§7, after the `context:` paragraph: "Task C.2 adds `policy.include` (policy
files, `PolicyFile{include, rules}`, paths relative to the naming file, no
`default`), `args` matchers and tool globs in rules, and
`Policy.Included []IncludedRule`; `PolicyEngine` evaluates included rules
(depth first, each file once) before the agent's rules."

## Steps

1. Branch from latest `main`.
2. `match.go` + `match_test.go`; `go test ./internal/policy/`.
3. `static.go` (`Args`, `compiled`, matching) + new static tests; existing tests pass unchanged.
4. `agentconfig.go` types, `include.go`, `validate.go`, `convert.go`, the three testdata files; tests.
5. `internal/cli/policy.go`, `root.go`, `policy_test.go`.
6. `interfaces.md`. `make fmt && make ci`. See each new test fail once.

## Tests

`internal/policy/match_test.go` and `static_test.go`:

| Test | Asserts |
|------|---------|
| `TestGlob` | every row of the glob table above |
| `TestCheckGlobRejects` | `docs/[a]`, `{a,b}`, `a\b`, `x]` → exact "bad glob" texts |
| `TestArgMatcherCheck` | none set, `Glob`+`Prefix` set, `In: []string{}` → `set exactly one of glob, prefix, regex, in`; `Regex: "("` → text starts `bad regex "(": error parsing regexp`; each single valid field → nil |
| `TestStaticArgsMatch` | rule `{ID: "docs", Args: {"path": {Glob: "docs/**"}}, Decision: allow}`, default deny; table over `ArgumentsJSON`: `{"path":"docs/a"}` → docs; `{"path":"src/a"}`, `{}`, `{"path":1}`, `{"path":null}`, `{"path":["docs/a"]}`, `[1]`, `not json`, `""` → default |
| `TestStaticArgsAllMustMatch` | two args (`path` prefix `a/`, `mode` in `[r]`); only both matching selects the rule |
| `TestStaticArgMatchers` | prefix, regex (`a+` matches `aaa`, not `baa`: whole value), in |
| `TestStaticToolGlob` | `Tool: "fs__write_*"` matches `fs__write_file`, not `fs__read_file`; a plain name still matches only itself |
| `TestNewStaticRejectsBadMatchers` | `policy: rule "a": tool: bad glob ...`, `policy: rule "a": args.path: bad regex ...`, `policy: rule "a": args: empty argument name`, `policy: rule "a": args.path: set exactly one of ...` |
| `TestNewStaticCopiesArgs` | changing the caller's `Args` map after `NewStatic` does not change decisions |

`internal/agentconfig/include_test.go` (write ad-hoc files in `t.TempDir()`; `Load` them):

| Test | Asserts |
|------|---------|
| `TestPolicyIncludeOrder` | `Load("testdata/policy/agent.yaml")`; `PolicyEngine().Rules` ids = `no-keys, tmp-reads, known-files, docs-writes, other-writes, reads`; `Default` deny |
| `TestPolicyIncludeDecisions` | through `PolicyEngine().Authorize`: write `docs/a.md` → allow `docs-writes`; write `src/a.go` → needs_approval `other-writes`; read `docs/k.pem` → deny `no-keys`; read `tmp/x` → `tmp-reads`; read `go.mod` → `known-files`; read `a.txt` → `reads` |
| `TestPolicyIncludeMissingFile` | error contains `policy.include: open ` and `no such file or directory` |
| `TestPolicyIncludeCycle` | `a.yaml` includes `b.yaml` includes `a.yaml` → contains `policy.include: include cycle: ` + `a.yaml -> ` + `b.yaml -> ` + `a.yaml` (full paths) |
| `TestPolicyIncludeSelf` | a policy file including itself → `include cycle` |
| `TestPolicyIncludeDefaultRejected` | policy file with `default: allow` → contains `decode:` and `unknown field "default"` |
| `TestPolicyIncludeUnknownMatcher` | `args: {path: {suffix: x}}` in a policy file → `unknown field "suffix"` |
| `TestPolicyIncludeOnce` | a includes b and c, both include d → d's rules appear once, before b's own rules |
| `TestPolicyIncludeDuplicateID` | included `x` and agent rule `x` → `policy.rules[0].id: duplicate rule id "x"` |
| `TestPolicyIncludeBadRule` | included rule with bad glob → contains `shared.yaml: rules[0].args.path: bad glob` |
| `TestPolicyIncludeEmptyPath` | `include: [""]` → `policy.include: empty path` |

`TestValidateTable` new rows (agent file only): `rule tool glob`, `rule args regex`,
`rule args none`, `rule args unknown matcher` (`unknown field "suffix"`), with the texts from Validation.

`internal/cli/policy_test.go` (agent `../agentconfig/testdata/policy/agent.yaml`):

| Test | Asserts |
|------|---------|
| `TestPolicyCheckPrintsDecision` | the example above → exit 0, stdout exactly the four lines |
| `TestPolicyCheckNeedsApproval` | `--args '{"path":"src/a.go"}'` → `decision: needs_approval`, `rule:     other-writes` |
| `TestPolicyCheckUnknownTool` | `--tool x__y` → `decision: deny`, `rule:     unspecified-risk`, `risk:     unspecified` |
| `TestPolicyCheckRiskFlag` | `--tool x__y --risk read --args '{"path":"a"}'` → `rule:     reads` |
| `TestPolicyCheckBadArgs` | `--args '[1]'` → exit 1, stderr contains `--args must be a JSON object` |
| `TestPolicyCheckNeedsTool` | no `--tool` → exit 1 (cobra required flag) |

## Verify

```bash
go test ./internal/policy/ ./internal/agentconfig/ -v -run 'Glob|Arg|Static|Include|Validate|PolicyEngine|RepoAgents'  # all PASS
go test ./internal/cli/ -run PolicyCheck -v       # all PASS
go build -o bin/adlc ./cmd/adlc && bin/adlc policy check internal/agentconfig/testdata/policy/agent.yaml \
  --tool fs__write_file --args '{"path":"docs/a.md"}'       # prints the four lines of the example
make ci                                           # passes
```

## Out of scope

- Matching nested fields, numbers or booleans; matching on agent id or environment.
- `default` or `include` set from anywhere but the agent file / policy files; remote policy (OPA, Cedar, control plane).
- Brace alternatives and character classes in globs.
- Policy files under `agents/` (TestLoadRepoAgents loads every `.yaml` there as an agent file).
- `adlc policy check` without an agent file, or with unset `${VAR}` prices.

## Pitfalls

- `json.Unmarshal("null", &s)` succeeds and leaves `s == ""`: check that the raw value starts with `"` first.
- `regexp.MatchString` is unanchored; the regex matcher must compile `^(?:…)$`, globs `(?s)^…$`.
- Validate the user's regex with `regexp.Compile(m.Regex)` (not the anchored form) so the error shows their text.
- Iterate `Args` in sorted order everywhere (errors and matching) so results are deterministic.
- Check for a cycle **before** the "already loaded" check, or cycles are silently ignored.
- `Policy.Included` has `yaml:"-"`; never accept it from YAML.
- Keep `Static.Rules` exported and in evaluation order: `TestPolicyEngine` reads `Rules[2]`.
- Existing messages in `TestValidateTable` and `TestNewStaticValidation` must not change.

## Done checklist

- [ ] `ArgMatcher`, `CheckGlob`, tool globs and arg matching in `internal/policy`, with tests
- [ ] `policy.include` with cycle, missing-file, decode and duplicate-id errors as specified
- [ ] Existing policy and agentconfig tests pass unchanged; `TestLoadRepoAgents` passes
- [ ] `adlc policy check` registered and tested
- [ ] `interfaces.md` §4 and §7 updated
- [ ] `make ci` passes
