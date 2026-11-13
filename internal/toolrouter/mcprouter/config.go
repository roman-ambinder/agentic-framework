// Package mcprouter implements toolrouter.Router over MCP servers.
package mcprouter

import (
	"fmt"
	"regexp"
	"strings"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
)

var serverNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)

// ServerConfig describes one MCP server.
type ServerConfig struct {
	Name        string                      // namespace, e.g. "fs"
	Command     []string                    // argv; Command[0] is the program
	Env         map[string]string           // added to the parent's environment
	Dir         string                      // working directory; "" = parent's
	Risk        map[string]adlcv1.RiskClass // tool name (not namespaced) -> risk
	DefaultRisk adlcv1.RiskClass            // for tools not in Risk; UNSPECIFIED = none
}

// Validate checks the config itself (not the tools).
func (c ServerConfig) Validate() error {
	if !serverNameRE.MatchString(c.Name) || strings.Contains(c.Name, "__") {
		return fmt.Errorf("mcprouter: server name %q must match ^[a-z][a-z0-9_]{0,15}$ and not contain \"__\"", c.Name)
	}
	if len(c.Command) == 0 {
		return fmt.Errorf("mcprouter: server %q has no command", c.Name)
	}
	return nil
}
