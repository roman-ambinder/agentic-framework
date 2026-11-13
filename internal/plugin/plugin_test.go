package plugin

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// helperServer is the plugin that TestHelperPlugin serves.
type helperServer struct {
	adlcv1.UnimplementedHookServiceServer
	mode string
}

func (s *helperServer) PreTool(_ context.Context, _ *adlcv1.PreToolRequest) (*adlcv1.PreToolResponse, error) {
	switch s.mode {
	case "deny":
		return &adlcv1.PreToolResponse{Decision: adlcv1.Decision_DECISION_DENY, Reason: "denied by helper"}, nil
	case "approval":
		return &adlcv1.PreToolResponse{Decision: adlcv1.Decision_DECISION_NEEDS_APPROVAL}, nil
	case "crash":
		os.Exit(3)
	case "slow":
		time.Sleep(2 * time.Second)
	}
	return &adlcv1.PreToolResponse{}, nil
}

func (s *helperServer) PostTool(_ context.Context, req *adlcv1.PostToolRequest) (*adlcv1.PostToolResponse, error) {
	if s.mode == "record" {
		f, err := os.OpenFile(os.Getenv("ADLC_TEST_RECORD"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		defer f.Close()
		if _, err := f.WriteString(req.GetInfo().GetCall().GetName() + "\n"); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	return &adlcv1.PostToolResponse{}, nil
}

// TestHelperPlugin is not a test: the other tests run the test binary again
// with ADLC_TEST_PLUGIN_MODE set, and it then acts as a plugin.
func TestHelperPlugin(t *testing.T) {
	mode := os.Getenv("ADLC_TEST_PLUGIN_MODE")
	if mode == "" {
		t.Skip("helper process for other tests")
	}
	switch mode {
	case "never-listen":
		time.Sleep(time.Hour)
	case "exit-early":
		_, _ = os.Stderr.WriteString("boom\n")
		os.Exit(1)
	}
	lis, err := net.Listen("unix", os.Getenv(SocketEnv))
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(2)
	}
	srv := grpc.NewServer()
	adlcv1.RegisterHookServiceServer(srv, &helperServer{mode: mode})
	_ = srv.Serve(lis)
	os.Exit(0)
}

func helper(mode string, extraEnv ...string) Config {
	return Config{
		Name:    "helper",
		Command: []string{os.Args[0], "-test.run=^TestHelperPlugin$"},
		Env:     append([]string{"ADLC_TEST_PLUGIN_MODE=" + mode}, extraEnv...),
	}
}

var (
	testCall = model.ToolCall{ID: "c1", Name: "fs__read_file", Arguments: []byte(`{}`)}
	testTool = toolrouter.Tool{Server: "fs", Risk: adlcv1.RiskClass_RISK_CLASS_READ}
)

func startHelper(t *testing.T, cfg Config) *Process {
	t.Helper()
	p, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestPreToolDeny(t *testing.T) {
	p := startHelper(t, helper("deny"))
	got, err := p.PreTool(context.Background(), testCall, testTool)
	if err != nil {
		t.Fatal(err)
	}
	if want := (harness.HookDecision{Deny: true, Reason: "denied by helper"}); got != want {
		t.Errorf("decision = %+v, want %+v", got, want)
	}
}

func TestPreToolUnspecifiedMeansAllow(t *testing.T) {
	p := startHelper(t, helper("allow"))
	got, err := p.PreTool(context.Background(), testCall, testTool)
	if err != nil {
		t.Fatal(err)
	}
	if got != (harness.HookDecision{}) {
		t.Errorf("decision = %+v, want zero", got)
	}
}

func TestNameIsConfigName(t *testing.T) {
	p := startHelper(t, helper("allow"))
	if p.Name() != "helper" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestPreToolUnsupportedDecision(t *testing.T) {
	p := startHelper(t, helper("approval"))
	_, err := p.PreTool(context.Background(), testCall, testTool)
	if err == nil || !strings.Contains(err.Error(), "unsupported decision") {
		t.Errorf("err = %v, want unsupported decision", err)
	}
}

func TestPostToolIsCalled(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec")
	p := startHelper(t, helper("record", "ADLC_TEST_RECORD="+rec))
	if err := p.PostTool(context.Background(), testCall, toolrouter.Result{Output: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "fs__read_file") {
		t.Errorf("record = %q", b)
	}
}

func TestStartTimeout(t *testing.T) {
	cfg := helper("never-listen")
	cfg.StartTimeout = 300 * time.Millisecond
	begin := time.Now()
	_, err := Start(context.Background(), cfg)
	if !errors.Is(err, ErrStartTimeout) {
		t.Fatalf("err = %v, want ErrStartTimeout", err)
	}
	if d := time.Since(begin); d > 2*time.Second {
		t.Errorf("Start took %s", d)
	}
}

func TestStartContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Start(ctx, helper("never-listen"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestExitBeforeListening(t *testing.T) {
	_, err := Start(context.Background(), helper("exit-early"))
	if !errors.Is(err, ErrExited) {
		t.Fatalf("err = %v, want ErrExited", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q lacks plugin output", err)
	}
}

func TestCrashDuringCall(t *testing.T) {
	p := startHelper(t, helper("crash"))
	_, err := p.PreTool(context.Background(), testCall, testTool)
	if !errors.Is(err, ErrExited) {
		t.Fatalf("first call err = %v, want ErrExited", err)
	}
	begin := time.Now()
	_, err = p.PreTool(context.Background(), testCall, testTool)
	if !errors.Is(err, ErrExited) {
		t.Errorf("second call err = %v, want ErrExited", err)
	}
	if time.Since(begin) > 100*time.Millisecond {
		t.Errorf("second call was not immediate: %s", time.Since(begin))
	}
}

func TestCloseStopsProcess(t *testing.T) {
	p := startHelper(t, helper("allow"))
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	default:
		t.Error("process still running after Close")
	}
	if _, err := os.Stat(p.dir); !os.IsNotExist(err) {
		t.Errorf("socket dir still exists: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close = %v", err)
	}
	if _, err := p.PreTool(context.Background(), testCall, testTool); !errors.Is(err, ErrClosed) {
		t.Errorf("PreTool after Close = %v, want ErrClosed", err)
	}
}

func TestCallTimeout(t *testing.T) {
	cfg := helper("slow")
	cfg.CallTimeout = 200 * time.Millisecond
	p := startHelper(t, cfg)
	begin := time.Now()
	_, err := p.PreTool(context.Background(), testCall, testTool)
	if d := time.Since(begin); d > time.Second {
		t.Errorf("call took %s", d)
	}
	if !errors.Is(err, context.DeadlineExceeded) && status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
}

func TestInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"empty command", Config{Name: "ok"}},
		{"bad name", Config{Name: "Bad Name", Command: []string{"true"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Start(context.Background(), tt.cfg)
			if err == nil || !strings.Contains(err.Error(), "invalid config") {
				t.Errorf("Start = %v, %v; want invalid config error", p, err)
			}
		})
	}
}
