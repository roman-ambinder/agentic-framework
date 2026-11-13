// Package agentconfig reads and validates agent definitions (agent.yaml).
package agentconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/goccy/go-yaml"
)

// Defaults used when a field is empty or zero.
const (
	DefaultMaxTokens          = 4096
	DefaultMaxToolOutputBytes = 16 * 1024
	DefaultFakeModel          = "fake-model"
	DefaultSandboxImage       = "golang:1.26"
)

// Agent is a parsed agent definition.
type Agent struct {
	Name               string       `yaml:"name"`
	Version            int          `yaml:"version"`
	Model              Model        `yaml:"model"`
	System             string       `yaml:"system"`
	SystemFile         string       `yaml:"system_file"`
	MaxToolOutputBytes int          `yaml:"max_tool_output_bytes"`
	Tools              []ToolServer `yaml:"tools"`
	Budget             Budget       `yaml:"budget"`
	Context            Context      `yaml:"context"`
	Policy             Policy       `yaml:"policy"`
	Hooks              []Hook       `yaml:"hooks"`
	// Sandbox, if set, makes `adlc run` create a sandbox for the run.
	Sandbox *Sandbox `yaml:"sandbox"`

	// Dir is the directory of the file Load read ("" for Parse). Not in YAML.
	Dir string `yaml:"-"`
}

// Sandbox describes the container `adlc run` creates for the agent.
type Sandbox struct {
	Image     string  `yaml:"image"`     // default DefaultSandboxImage
	Workspace string  `yaml:"workspace"` // host dir, relative to the agent file
	Network   bool    `yaml:"network"`
	CPUs      float64 `yaml:"cpus"`      // 0 = no limit
	MemoryMB  int64   `yaml:"memory_mb"` // 0 = no limit
}

// Model selects and configures the model.
type Model struct {
	Provider  string  `yaml:"provider"`
	Name      string  `yaml:"name"`
	MaxTokens int     `yaml:"max_tokens"`
	Effort    string  `yaml:"effort"`
	Pricing   Pricing `yaml:"pricing"`
	// Fallbacks are tried in order when the primary fails with a retryable
	// error (task 1.3). A fallback has no fallbacks of its own.
	Fallbacks []Model `yaml:"fallbacks"`
}

// Pricing holds USD per million tokens as decimal strings.
type Pricing struct {
	InputPerMTok      string `yaml:"input_per_mtok"`
	OutputPerMTok     string `yaml:"output_per_mtok"`
	CacheReadPerMTok  string `yaml:"cache_read_per_mtok"`
	CacheWritePerMTok string `yaml:"cache_write_per_mtok"`
}

// ToolServer describes one tool server process.
type ToolServer struct {
	Server      string            `yaml:"server"`
	Command     []string          `yaml:"command"`
	Env         map[string]string `yaml:"env"`
	Risk        map[string]string `yaml:"risk"`
	DefaultRisk string            `yaml:"default_risk"`
}

// Hook declares one out-of-process hook plugin (internal/plugin). It is
// started in the agent file's directory.
type Hook struct {
	Name         string            `yaml:"name"`
	Command      []string          `yaml:"command"`
	Env          map[string]string `yaml:"env"`
	StartTimeout string            `yaml:"start_timeout"` // Go duration; "" = plugin default
	CallTimeout  string            `yaml:"call_timeout"`  // Go duration; "" = plugin default
}

// Budget holds run limits; zero or empty means no limit.
type Budget struct {
	MaxSteps     uint32 `yaml:"max_steps"`
	MaxTokens    int64  `yaml:"max_tokens"`
	MaxWallClock string `yaml:"max_wall_clock"`
	MaxUSD       string `yaml:"max_usd"`
	MaxToolCalls uint32 `yaml:"max_tool_calls"`
}

// Context limits the size of each model request; zero means the default.
type Context struct {
	MaxInputTokens  int64 `yaml:"max_input_tokens"`  // 0 = derived from the model
	KeepRecentSteps int   `yaml:"keep_recent_steps"` // 0 = 2
}

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

// Load reads, decodes (strict), applies defaults and validates the file.
func Load(path string) (*Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agentconfig: %w", err)
	}
	abs, _ := filepath.Abs(path)
	a, err := parse(data, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("agentconfig: %s: %w", path, err)
	}
	return a, nil
}

// Parse does the same as Load for bytes; Dir stays "".
func Parse(data []byte) (*Agent, error) {
	a, err := parse(data, "")
	if err != nil {
		return nil, fmt.Errorf("agentconfig: %w", err)
	}
	return a, nil
}

func parse(data []byte, dir string) (*Agent, error) {
	var a Agent
	if err := yaml.UnmarshalWithOptions(data, &a, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("decode: %s", yaml.FormatError(err, false, true))
	}
	a.Dir = dir
	a.applyDefaults()
	if err := a.resolve(); err != nil {
		return nil, err
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return &a, nil
}

func (a *Agent) applyDefaults() {
	if a.Model.MaxTokens == 0 {
		a.Model.MaxTokens = DefaultMaxTokens
	}
	if a.MaxToolOutputBytes == 0 {
		a.MaxToolOutputBytes = DefaultMaxToolOutputBytes
	}
	if a.Sandbox != nil && a.Sandbox.Image == "" {
		a.Sandbox.Image = DefaultSandboxImage
	}
	if a.Model.Provider == "fake" && a.Model.Name == "" {
		a.Model.Name = DefaultFakeModel
	}
}

var envNameRE = regexp.MustCompile(`^\$\{([A-Z_][A-Z0-9_]*)\}$`)

// resolve expands model names and prices from the environment and loads
// system_file.
func (a *Agent) resolve() error {
	var p problems
	expandModel(&p, "model", &a.Model)
	for i := range a.Model.Fallbacks {
		expandModel(&p, fmt.Sprintf("model.fallbacks[%d]", i), &a.Model.Fallbacks[i])
	}
	a.resolveSystemFile(&p)
	a.resolvePolicyIncludes(&p)
	return errors.Join(p...)
}

// expandModel expands the name and every pricing field of m. path is "model"
// or "model.fallbacks[i]".
func expandModel(p *problems, path string, m *Model) {
	expandName(p, path+".name", &m.Name)
	pr := &m.Pricing
	expandName(p, path+".pricing.input_per_mtok", &pr.InputPerMTok)
	expandName(p, path+".pricing.output_per_mtok", &pr.OutputPerMTok)
	expandName(p, path+".pricing.cache_read_per_mtok", &pr.CacheReadPerMTok)
	expandName(p, path+".pricing.cache_write_per_mtok", &pr.CacheWritePerMTok)
}

// expandName replaces a value that is exactly ${VAR} with the variable's value.
func expandName(p *problems, path string, name *string) {
	m := envNameRE.FindStringSubmatch(*name)
	if m == nil {
		return
	}
	v, ok := os.LookupEnv(m[1])
	if !ok || v == "" {
		p.add(path, "environment variable %s is not set", m[1])
		return
	}
	*name = v
}

func (a *Agent) resolveSystemFile(p *problems) {
	if a.SystemFile == "" {
		return
	}
	if a.System != "" {
		p.add("system_file", "cannot be used together with system")
		return
	}
	path := a.SystemFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.Dir, path)
	}
	content, err := os.ReadFile(path)
	switch {
	case err != nil:
		p.add("system_file", "%v", err)
	case len(content) == 0:
		p.add("system_file", "file is empty")
	default:
		a.System = string(content)
	}
}
