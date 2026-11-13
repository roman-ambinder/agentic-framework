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
	Policy             Policy       `yaml:"policy"`

	// Dir is the directory of the file Load read ("" for Parse). Not in YAML.
	Dir string `yaml:"-"`
}

// Model selects and configures the model.
type Model struct {
	Provider  string  `yaml:"provider"`
	Name      string  `yaml:"name"`
	MaxTokens int     `yaml:"max_tokens"`
	Effort    string  `yaml:"effort"`
	Pricing   Pricing `yaml:"pricing"`
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

// Budget holds run limits; zero or empty means no limit.
type Budget struct {
	MaxSteps     uint32 `yaml:"max_steps"`
	MaxTokens    int64  `yaml:"max_tokens"`
	MaxWallClock string `yaml:"max_wall_clock"`
	MaxUSD       string `yaml:"max_usd"`
	MaxToolCalls uint32 `yaml:"max_tool_calls"`
}

// Policy is the static policy: a default decision and ordered rules.
type Policy struct {
	Default string `yaml:"default"`
	Rules   []Rule `yaml:"rules"`
}

// Rule is one policy rule; the first match wins.
type Rule struct {
	ID       string `yaml:"id"`
	Risk     string `yaml:"risk"`
	Tool     string `yaml:"tool"`
	Decision string `yaml:"decision"`
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
	if a.Model.Provider == "fake" && a.Model.Name == "" {
		a.Model.Name = DefaultFakeModel
	}
}

var envNameRE = regexp.MustCompile(`^\$\{([A-Z_][A-Z0-9_]*)\}$`)

// resolve expands model.name from the environment and loads system_file.
func (a *Agent) resolve() error {
	var p problems
	if m := envNameRE.FindStringSubmatch(a.Model.Name); m != nil {
		if v, ok := os.LookupEnv(m[1]); !ok || v == "" {
			p.add("model.name", "environment variable %s is not set", m[1])
		} else {
			a.Model.Name = v
		}
	}
	a.resolveSystemFile(&p)
	return errors.Join(p...)
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
