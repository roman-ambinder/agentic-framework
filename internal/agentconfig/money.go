package agentconfig

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

var moneyRE = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(\.([0-9]{1,6}))?$`)

// ParseMicroUSD parses a decimal USD amount ("1", "1.5", "0.000001") into
// micro-USD without floating point. "" -> 0. Max 6 decimals, no sign, no
// exponent, at most 9 digits before the point.
func ParseMicroUSD(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	m := moneyRE.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("agentconfig: %q is not a decimal amount with at most 6 decimals", s)
	}
	whole, _ := strconv.ParseInt(m[1], 10, 64)
	frac := m[3] + strings.Repeat("0", 6-len(m[3]))
	f, _ := strconv.ParseInt(frac, 10, 64)
	return whole*1_000_000 + f, nil
}

// moneyText is a money value exactly as written in the agent file, quoted or
// not. Decoding an unquoted number into a string field would go through a
// float or an integer first (010 becomes "8", 0.000001 becomes "1e-06"), so
// the scalar's source text is kept and ParseMicroUSD checks it.
type moneyText string

// UnmarshalYAML keeps the scalar's source text (goccy/go-yaml NodeUnmarshaler).
func (m *moneyText) UnmarshalYAML(n ast.Node) error {
	switch n.Type() {
	case ast.StringType, ast.IntegerType, ast.FloatType:
		*m = moneyText(n.GetToken().Value)
		return nil
	case ast.NullType:
		*m = ""
		return nil
	default:
		return fmt.Errorf("a money value must be a number or a string, got %s", n.Type())
	}
}

// pricingYAML is Pricing with its money fields read as source text.
type pricingYAML struct {
	InputPerMTok      moneyText `yaml:"input_per_mtok"`
	OutputPerMTok     moneyText `yaml:"output_per_mtok"`
	CacheReadPerMTok  moneyText `yaml:"cache_read_per_mtok"`
	CacheWritePerMTok moneyText `yaml:"cache_write_per_mtok"`
}

// UnmarshalYAML decodes Pricing, keeping each price as written.
func (p *Pricing) UnmarshalYAML(n ast.Node) error {
	var raw pricingYAML
	if err := yaml.NodeToValue(n, &raw, yaml.Strict()); err != nil {
		return err
	}
	*p = Pricing{
		InputPerMTok: string(raw.InputPerMTok), OutputPerMTok: string(raw.OutputPerMTok),
		CacheReadPerMTok: string(raw.CacheReadPerMTok), CacheWritePerMTok: string(raw.CacheWritePerMTok),
	}
	return nil
}

// budgetYAML is Budget with max_usd read as source text.
type budgetYAML struct {
	MaxSteps     uint32    `yaml:"max_steps"`
	MaxTokens    int64     `yaml:"max_tokens"`
	MaxWallClock string    `yaml:"max_wall_clock"`
	MaxUSD       moneyText `yaml:"max_usd"`
	MaxToolCalls uint32    `yaml:"max_tool_calls"`
}

// UnmarshalYAML decodes Budget, keeping max_usd as written.
func (b *Budget) UnmarshalYAML(n ast.Node) error {
	var raw budgetYAML
	if err := yaml.NodeToValue(n, &raw, yaml.Strict()); err != nil {
		return err
	}
	*b = Budget{
		MaxSteps: raw.MaxSteps, MaxTokens: raw.MaxTokens, MaxWallClock: raw.MaxWallClock,
		MaxUSD: string(raw.MaxUSD), MaxToolCalls: raw.MaxToolCalls,
	}
	return nil
}
