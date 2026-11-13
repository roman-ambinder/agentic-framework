package agentconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

// includeLoader loads policy files depth first; each file is loaded once.
type includeLoader struct {
	seen  map[string]bool // absolute paths already loaded (or being loaded)
	rules []IncludedRule
}

// resolvePolicyIncludes loads every file named by policy.include and stores
// their rules in a.Policy.Included, in evaluation order.
func (a *Agent) resolvePolicyIncludes(p *problems) {
	l := &includeLoader{seen: map[string]bool{}}
	for _, inc := range a.Policy.Include {
		l.load(p, a.Dir, inc, nil)
	}
	a.Policy.Included = l.rules
}

// load reads one policy file and, before its own rules, the files it includes.
// stack holds the absolute paths being loaded, outermost first.
func (l *includeLoader) load(p *problems, baseDir, entry string, stack []string) {
	if entry == "" {
		p.add("policy.include", "empty path")
		return
	}
	f := entry
	if !filepath.IsAbs(f) {
		f = filepath.Join(baseDir, f)
	}
	f, err := filepath.Abs(f)
	if err != nil {
		p.add("policy.include", "%v", err)
		return
	}
	if k := slices.Index(stack, f); k >= 0 {
		cycle := append(slices.Clone(stack[k:]), f)
		p.add("policy.include", "include cycle: %s", strings.Join(cycle, " -> "))
		return
	}
	if l.seen[f] {
		return
	}
	l.seen[f] = true
	data, err := os.ReadFile(f)
	if err != nil {
		p.add("policy.include", "%v", err)
		return
	}
	var pf PolicyFile
	if err := yaml.UnmarshalWithOptions(data, &pf, yaml.Strict()); err != nil {
		p.add("policy.include", "%s: decode: %s", f, yaml.FormatError(err, false, true))
		return
	}
	next := append(slices.Clone(stack), f)
	for _, inc := range pf.Include {
		l.load(p, filepath.Dir(f), inc, next)
	}
	for i, r := range pf.Rules {
		l.rules = append(l.rules, IncludedRule{File: f, Index: i, Rule: r})
	}
}
