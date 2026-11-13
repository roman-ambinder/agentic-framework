package sandboxset_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox"
	"github.com/roman-ambinder/agentic-framework/internal/sandbox/sandboxtest"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxset"
	"github.com/roman-ambinder/agentic-framework/internal/tools/sandboxtool"
)

func TestNewRouterTools(t *testing.T) {
	ctx := context.Background()
	host := sandboxtest.NewHost(t.TempDir())
	h, err := host.Create(ctx, sandbox.Spec{RunID: "t", Image: "host"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := sandboxset.NewRouter(ctx, sandboxtool.Options{Sandbox: host, Handle: h})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]adlcv1.RiskClass{
		"shell__run":        adlcv1.RiskClass_RISK_CLASS_EXEC,
		"git__status":       adlcv1.RiskClass_RISK_CLASS_READ,
		"git__diff":         adlcv1.RiskClass_RISK_CLASS_READ,
		"search__grep":      adlcv1.RiskClass_RISK_CLASS_READ,
		"files__read_file":  adlcv1.RiskClass_RISK_CLASS_READ,
		"files__write_file": adlcv1.RiskClass_RISK_CLASS_WRITE_LOCAL,
		"files__list_dir":   adlcv1.RiskClass_RISK_CLASS_READ,
	}
	got := map[string]adlcv1.RiskClass{}
	for _, tool := range r.Tools() {
		got[tool.Spec.Name] = tool.Risk
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	res, err := r.Call(ctx, model.ToolCall{ID: "1", Name: "shell__run", Arguments: json.RawMessage(`{"command":"echo ok"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(string(res.Output), "ok") {
		t.Fatalf("result = %+v", res)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestServerConfigsCommand(t *testing.T) {
	cfgs := sandboxset.ServerConfigs("c1")
	if want := []string{"adlc", "tool", "shell", "--sandbox", "c1"}; !reflect.DeepEqual(cfgs[0].Command, want) {
		t.Fatalf("Command = %q, want %q", cfgs[0].Command, want)
	}
	var names []string
	for _, c := range cfgs {
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
		names = append(names, c.Name)
	}
	sort.Strings(names)
	if want := []string{"files", "git", "search", "shell"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

// TestCoderPromptListsRouterTools checks that the coder prompt lists exactly
// the tools the router exposes, each with the first sentence of its description.
func TestCoderPromptListsRouterTools(t *testing.T) {
	ctx := context.Background()
	host := sandboxtest.NewHost(t.TempDir())
	h, err := host.Create(ctx, sandbox.Spec{RunID: "t", Image: "host"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := sandboxset.NewRouter(ctx, sandboxtool.Options{Sandbox: host, Handle: h})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	prompt, err := os.ReadFile("../../../agents/sdlc/coder.prompt.md")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, line := range strings.Split(string(prompt), "\n") {
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "__") {
			listed = append(listed, line)
		}
	}
	var want []string
	for _, tool := range r.Tools() {
		first, _, _ := strings.Cut(tool.Spec.Description, ". ")
		want = append(want, "- "+tool.Spec.Name+": "+strings.TrimSuffix(first, ".")+".")
	}
	sort.Strings(listed)
	sort.Strings(want)
	if !reflect.DeepEqual(listed, want) {
		t.Fatalf("prompt tool lines:\n%q\nwant:\n%q", listed, want)
	}
}
