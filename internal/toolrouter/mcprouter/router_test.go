package mcprouter

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

const (
	riskRead = adlcv1.RiskClass_RISK_CLASS_READ
	riskExec = adlcv1.RiskClass_RISK_CLASS_EXEC
)

type echoIn struct {
	Text string `json:"text"`
}
type echoOut struct {
	Echo string `json:"echo"`
}

func newPrefixServer(prefix string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "echoes"},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, echoOut, error) {
			return nil, echoOut{Echo: prefix + in.Text}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "fails"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ echoIn) (*mcp.CallToolResult, echoOut, error) {
			return nil, echoOut{}, errors.New("it broke")
		})
	mcp.AddTool(s, &mcp.Tool{Name: "image", Description: "image"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ echoIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte{1}, MIMEType: "image/png"}}}, nil, nil
		})
	return s
}

// newTestServer returns a server with tools echo, fail and image.
func newTestServer() *mcp.Server { return newPrefixServer("") }

func connectTest(t *testing.T, servers map[string]*mcp.Server, cfg map[string]ServerConfig) (*Router, error) {
	t.Helper()
	ctx := context.Background()
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	var conns []Conn
	for _, n := range names {
		ct, st := mcp.NewInMemoryTransports()
		if _, err := servers[n].Connect(ctx, st, nil); err != nil {
			t.Fatalf("server connect: %v", err)
		}
		conns = append(conns, Conn{Config: cfg[n], Transport: ct})
	}
	r, err := Connect(ctx, conns)
	if err == nil {
		t.Cleanup(func() { _ = r.Close() })
	}
	return r, err
}

func oneServer(t *testing.T, cfg ServerConfig) (*Router, error) {
	t.Helper()
	cfg.Name = "t"
	return connectTest(t, map[string]*mcp.Server{"t": newTestServer()}, map[string]ServerConfig{"t": cfg})
}

func mustRouter(t *testing.T, cfg ServerConfig) *Router {
	t.Helper()
	r, err := oneServer(t, cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return r
}

func call(t *testing.T, r *Router, name, args string) toolrouter.Result {
	t.Helper()
	var raw []byte
	if args != "" {
		raw = []byte(args)
	}
	res, err := r.Call(context.Background(), model.ToolCall{ID: "1", Name: name, Arguments: raw})
	if err != nil {
		t.Fatalf("Call %s: %v", name, err)
	}
	return res
}

func TestConnectListsNamespacedTools(t *testing.T) {
	r := mustRouter(t, ServerConfig{DefaultRisk: riskRead})
	var names []string
	for _, tl := range r.Tools() {
		names = append(names, tl.Spec.Name)
		if tl.Server != "t" {
			t.Errorf("tool %s Server = %q, want t", tl.Spec.Name, tl.Server)
		}
		if tl.Spec.Name == "t__echo" && !strings.Contains(string(tl.Spec.InputSchema), `"text"`) {
			t.Errorf("echo schema = %s, want it to contain \"text\"", tl.Spec.InputSchema)
		}
	}
	// The SDK lists tools sorted by name.
	if want := []string{"t__echo", "t__fail", "t__image"}; !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestConnectRiskFromConfig(t *testing.T) {
	r := mustRouter(t, ServerConfig{Risk: map[string]adlcv1.RiskClass{"echo": riskExec}, DefaultRisk: riskRead})
	for _, tl := range r.Tools() {
		want := riskRead
		if tl.Spec.Name == "t__echo" {
			want = riskExec
		}
		if tl.Risk != want {
			t.Errorf("%s risk = %v, want %v", tl.Spec.Name, tl.Risk, want)
		}
	}
}

func TestConnectMissingRisk(t *testing.T) {
	_, err := oneServer(t, ServerConfig{})
	if err == nil || !strings.Contains(err.Error(), "has no risk class") {
		t.Fatalf("err = %v, want \"has no risk class\"", err)
	}
}

func TestConnectRiskTypo(t *testing.T) {
	_, err := oneServer(t, ServerConfig{Risk: map[string]adlcv1.RiskClass{"ecoh": riskRead}, DefaultRisk: riskRead})
	if err == nil || !strings.Contains(err.Error(), `has no tool "ecoh"`) {
		t.Fatalf("err = %v, want `has no tool \"ecoh\"`", err)
	}
}

func TestConnectDuplicateServer(t *testing.T) {
	ctx := context.Background()
	var conns []Conn
	for range 2 {
		ct, st := mcp.NewInMemoryTransports()
		if _, err := newTestServer().Connect(ctx, st, nil); err != nil {
			t.Fatal(err)
		}
		conns = append(conns, Conn{Config: ServerConfig{Name: "t", DefaultRisk: riskRead}, Transport: ct})
	}
	_, err := Connect(ctx, conns)
	if err == nil || !strings.Contains(err.Error(), "duplicate server") {
		t.Fatalf("err = %v, want duplicate server", err)
	}
}

func TestCallEcho(t *testing.T) {
	r := mustRouter(t, ServerConfig{DefaultRisk: riskRead})
	res := call(t, r, "t__echo", `{"text":"hi"}`)
	if string(res.Output) != `{"echo":"hi"}` || res.IsError {
		t.Fatalf("result = %q, IsError %v; want {\"echo\":\"hi\"}, false", res.Output, res.IsError)
	}
	if res.Duration <= 0 {
		t.Fatalf("Duration = %v, want > 0", res.Duration)
	}
}

func TestCallToolFailure(t *testing.T) {
	r := mustRouter(t, ServerConfig{DefaultRisk: riskRead})
	res := call(t, r, "t__fail", `{"text":"x"}`)
	if !res.IsError || !strings.Contains(string(res.Output), "it broke") {
		t.Fatalf("result = %q, IsError %v; want error containing \"it broke\"", res.Output, res.IsError)
	}
}

func TestCallInvalidArguments(t *testing.T) {
	r := mustRouter(t, ServerConfig{DefaultRisk: riskRead})
	res := call(t, r, "t__echo", `{"text":5}`)
	if !res.IsError || !strings.Contains(string(res.Output), "validating") {
		t.Fatalf("result = %q, IsError %v; want validating error", res.Output, res.IsError)
	}
}

func TestCallNonTextContent(t *testing.T) {
	r := mustRouter(t, ServerConfig{DefaultRisk: riskRead})
	res := call(t, r, "t__image", `{"text":"x"}`)
	if string(res.Output) != "[imagecontent content omitted]" {
		t.Fatalf("Output = %q", res.Output)
	}
}

func TestCallUnknownTool(t *testing.T) {
	r := mustRouter(t, ServerConfig{DefaultRisk: riskRead})
	_, err := r.Call(context.Background(), model.ToolCall{Name: "t__nope"})
	if !errors.Is(err, toolrouter.ErrUnknownTool) {
		t.Fatalf("err = %v, want ErrUnknownTool", err)
	}
}

func TestCallEmptyArgumentsSendsObject(t *testing.T) {
	r := mustRouter(t, ServerConfig{DefaultRisk: riskRead})
	res := call(t, r, "t__echo", "")
	if !res.IsError || !strings.Contains(string(res.Output), "missing properties") {
		t.Fatalf("result = %q, IsError %v; want missing properties error", res.Output, res.IsError)
	}
}

func TestCloseTwice(t *testing.T) {
	r := mustRouter(t, ServerConfig{DefaultRisk: riskRead})
	if err := r.Close(); err != nil {
		t.Fatalf("first Close = %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

func TestTwoServers(t *testing.T) {
	cfg := map[string]ServerConfig{
		"a": {Name: "a", DefaultRisk: riskRead},
		"b": {Name: "b", DefaultRisk: riskRead},
	}
	r, err := connectTest(t, map[string]*mcp.Server{"a": newPrefixServer("a:"), "b": newPrefixServer("b:")}, cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	var names []string
	for _, tl := range r.Tools() {
		names = append(names, tl.Spec.Name)
	}
	want := []string{"a__echo", "a__fail", "a__image", "b__echo", "b__fail", "b__image"}
	if !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if got := string(call(t, r, "a__echo", `{"text":"x"}`).Output); got != `{"echo":"a:x"}` {
		t.Errorf("a__echo = %s", got)
	}
	if got := string(call(t, r, "b__echo", `{"text":"x"}`).Output); got != `{"echo":"b:x"}` {
		t.Errorf("b__echo = %s", got)
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("MCPROUTER_TEST_SERVER") == "1" {
		if err := newTestServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartSubprocess(t *testing.T) {
	r, err := Start(context.Background(), []ServerConfig{{
		Name:        "t",
		Command:     []string{os.Args[0]},
		Env:         map[string]string{"MCPROUTER_TEST_SERVER": "1"},
		DefaultRisk: riskRead,
	}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	res := call(t, r, "t__echo", `{"text":"sub"}`)
	if string(res.Output) != `{"echo":"sub"}` {
		t.Fatalf("Output = %q", res.Output)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestStartRejectsInvalidConfig(t *testing.T) {
	_, err := Start(context.Background(), []ServerConfig{{Name: "BAD", Command: []string{"x"}}})
	if err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("err = %v, want name error", err)
	}
}
