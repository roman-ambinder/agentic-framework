package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/agentconfig"
	"github.com/roman-ambinder/agentic-framework/internal/policy"
)

// policyCheckOptions are the flags of `adlc policy check`.
type policyCheckOptions struct {
	agentPath, tool, args, risk, modelName string
}

func newPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect an agent's policy",
	}
	cmd.AddCommand(newPolicyCheckCmd())
	return cmd
}

func newPolicyCheckCmd() *cobra.Command {
	var o policyCheckOptions
	cmd := &cobra.Command{
		Use:   "check AGENT_YAML --tool NAME [--args JSON]",
		Short: "Print the policy decision for one tool call",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.agentPath = args[0]
			return policyCheck(cmd.Context(), o, cmd.OutOrStdout())
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.tool, "tool", "", "namespaced tool name, for example fs__write_file (required)")
	f.StringVar(&o.args, "args", "{}", "tool arguments as a JSON object")
	f.StringVar(&o.risk, "risk", "", "risk class (default: from the agent's tool declarations)")
	f.StringVar(&o.modelName, "model-name", "", "overrides model.name from the agent file")
	_ = cmd.MarkFlagRequired("tool")
	return cmd
}

// policyCheck prints the decision of the agent's policy for one tool call.
func policyCheck(ctx context.Context, o policyCheckOptions, stdout io.Writer) error {
	a, err := loadAgent(runOptions{agentPath: o.agentPath, modelName: o.modelName})
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	args := strings.TrimSpace(o.args)
	if !json.Valid([]byte(args)) || !strings.HasPrefix(args, "{") {
		return &exitError{code: 1, err: fmt.Errorf("policy check: --args must be a JSON object")}
	}
	risk := toolRisk(a, o.tool)
	if o.risk != "" {
		if risk, err = policy.ParseRiskClass(o.risk); err != nil {
			return &exitError{code: 1, err: fmt.Errorf("policy check: --risk: %w", err)}
		}
	}
	d, err := a.PolicyEngine().Authorize(ctx, policy.Request{
		AgentID: a.AgentID(), Tool: o.tool, Risk: risk, ArgumentsJSON: args,
	})
	if err != nil {
		return &exitError{code: 1, err: fmt.Errorf("policy check: %w", err)}
	}
	for _, l := range [][2]string{
		{"decision:", policy.DecisionName(d.Decision)},
		{"rule:", d.RuleID},
		{"reason:", d.Reason},
		{"risk:", policy.RiskClassName(risk)},
	} {
		fmt.Fprintf(stdout, "%-10s%s\n", l[0], l[1])
	}
	return nil
}

// toolRisk returns the risk class the agent file gives the namespaced tool,
// or UNSPECIFIED when the name has no "__" or names no declared server.
func toolRisk(a *agentconfig.Agent, tool string) adlcv1.RiskClass {
	server, name, ok := strings.Cut(tool, "__")
	if !ok {
		return adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED
	}
	for _, s := range a.ToolServers() {
		if s.Name != server {
			continue
		}
		if r, ok := s.Risk[name]; ok {
			return r
		}
		return s.DefaultRisk
	}
	return adlcv1.RiskClass_RISK_CLASS_UNSPECIFIED
}
