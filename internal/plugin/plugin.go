// Package plugin runs out-of-process hook plugins and exposes each one as a
// harness.Hook. A plugin is a program that serves adlc.v1.HookService on the
// Unix socket named by the ADLC_PLUGIN_SOCKET environment variable.
package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	adlcv1 "github.com/roman-ambinder/agentic-framework/gen/go/adlc/v1"
	"github.com/roman-ambinder/agentic-framework/internal/harness"
	"github.com/roman-ambinder/agentic-framework/internal/model"
	"github.com/roman-ambinder/agentic-framework/internal/toolrouter"
)

// SocketEnv is the environment variable that tells a plugin where to listen.
const SocketEnv = "ADLC_PLUGIN_SOCKET"

var (
	// ErrStartTimeout means the plugin did not accept connections in time.
	ErrStartTimeout = errors.New("plugin: start timeout")
	// ErrExited means the plugin process is not running (before or during a call).
	ErrExited = errors.New("plugin: process exited")
	// ErrClosed means the Process was closed.
	ErrClosed = errors.New("plugin: closed")
)

const (
	defaultStartTimeout = 10 * time.Second
	defaultCallTimeout  = 5 * time.Second
	maxSocketPath       = 100
	termGrace           = 3 * time.Second
	exitWait            = 200 * time.Millisecond
	pollInterval        = 20 * time.Millisecond
	tailSize            = 8 << 10
)

var namePattern = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// Config describes how to start a plugin.
type Config struct {
	Name         string        // hook name, used in events, errors and the socket file name; ^[a-z0-9-]{1,40}$
	Command      []string      // argv; Command[0] is the program
	Env          []string      // extra "KEY=VALUE" entries, appended to os.Environ()
	Dir          string        // working directory; "" = current
	StartTimeout time.Duration // 0 = 10s
	CallTimeout  time.Duration // 0 = 5s, per RPC
}

// Process is a running plugin. It implements harness.Hook.
type Process struct {
	cfg     Config
	dir     string
	cmd     *exec.Cmd
	out     *tailBuffer
	exited  chan struct{}
	waitErr error // valid after exited is closed
	conn    *grpc.ClientConn
	client  adlcv1.HookServiceClient

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
}

var _ harness.Hook = (*Process)(nil)

// tailBuffer keeps the last tailSize bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

// Write implements io.Writer.
func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > tailSize {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-tailSize:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.TrimSpace(t.buf))
}

func validate(cfg Config) error {
	if len(cfg.Command) == 0 {
		return errors.New("plugin: invalid config: empty command")
	}
	if !namePattern.MatchString(cfg.Name) {
		return fmt.Errorf("plugin: invalid config: name %q does not match %s", cfg.Name, namePattern)
	}
	return nil
}

// Start launches the plugin and waits until it accepts connections.
func Start(ctx context.Context, cfg Config) (*Process, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "adlcp-")
	if err != nil {
		return nil, fmt.Errorf("plugin %s: make socket dir: %w", cfg.Name, err)
	}
	sock := filepath.Join(dir, cfg.Name+".sock")
	if len(sock) > maxSocketPath {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("plugin %s: socket path too long (%d bytes)", cfg.Name, len(sock))
	}
	p := &Process{cfg: cfg, dir: dir, out: &tailBuffer{}, exited: make(chan struct{})}
	if err := p.launch(sock); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := p.waitListening(ctx, sock); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		p.stop()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("plugin %s: connect: %w", cfg.Name, err)
	}
	p.conn = conn
	p.client = adlcv1.NewHookServiceClient(conn)
	return p, nil
}

// launch starts the child. It is not tied to ctx: the plugin outlives Start.
func (p *Process) launch(sock string) error {
	cmd := exec.Command(p.cfg.Command[0], p.cfg.Command[1:]...)
	cmd.Dir = p.cfg.Dir
	cmd.Env = append(append(os.Environ(), SocketEnv+"="+sock), p.cfg.Env...)
	cmd.Stdout = p.out
	cmd.Stderr = p.out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin %s: start: %w", p.cfg.Name, err)
	}
	p.cmd = cmd
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
	}()
	return nil
}

// waitListening polls until the socket accepts a connection.
func (p *Process) waitListening(ctx context.Context, sock string) error {
	timeout := p.cfg.StartTimeout
	if timeout == 0 {
		timeout = defaultStartTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		if c, err := net.DialTimeout("unix", sock, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-p.exited:
			werr := p.waitErr
			if werr == nil {
				werr = errors.New("exit status 0")
			}
			return fmt.Errorf("plugin %s: %w before listening: %w; output: %s", p.cfg.Name, ErrExited, werr, p.out)
		case <-deadline.C:
			p.stop()
			return fmt.Errorf("plugin %s: %w after %s; output: %s", p.cfg.Name, ErrStartTimeout, timeout, p.out)
		case <-ctx.Done():
			p.stop()
			return fmt.Errorf("plugin %s: start: %w", p.cfg.Name, ctx.Err())
		case <-tick.C:
		}
	}
}

// stop kills the process and waits for it to be reaped.
func (p *Process) stop() {
	_ = p.cmd.Process.Kill()
	<-p.exited
}

// Name implements harness.Hook; it returns Config.Name.
func (p *Process) Name() string { return p.cfg.Name }

// PreTool implements harness.Hook.
func (p *Process) PreTool(ctx context.Context, call model.ToolCall, tool toolrouter.Tool) (harness.HookDecision, error) {
	if err := p.ready(); err != nil {
		return harness.HookDecision{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, p.callTimeout())
	defer cancel()
	resp, err := p.client.PreTool(cctx, &adlcv1.PreToolRequest{Info: toProtoInfo(call, tool)})
	if err != nil {
		return harness.HookDecision{}, p.callError("pre_tool", err)
	}
	switch resp.GetDecision() {
	case adlcv1.Decision_DECISION_UNSPECIFIED, adlcv1.Decision_DECISION_ALLOW:
		return harness.HookDecision{}, nil
	case adlcv1.Decision_DECISION_DENY:
		return harness.HookDecision{Deny: true, Reason: resp.GetReason()}, nil
	default:
		return harness.HookDecision{}, fmt.Errorf("plugin %s: unsupported decision %s", p.cfg.Name, resp.GetDecision())
	}
}

// PostTool implements harness.Hook.
func (p *Process) PostTool(ctx context.Context, call model.ToolCall, res toolrouter.Result) error {
	if err := p.ready(); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, p.callTimeout())
	defer cancel()
	if _, err := p.client.PostTool(cctx, toPostToolRequest(call, res)); err != nil {
		return p.callError("post_tool", err)
	}
	return nil
}

func (p *Process) callTimeout() time.Duration {
	if p.cfg.CallTimeout == 0 {
		return defaultCallTimeout
	}
	return p.cfg.CallTimeout
}

// ready returns an error if the process cannot take calls.
func (p *Process) ready() error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrClosed
	}
	select {
	case <-p.exited:
		return fmt.Errorf("plugin %s: %w", p.cfg.Name, ErrExited)
	default:
		return nil
	}
}

// callError wraps an RPC error; if the process died, it reports ErrExited.
func (p *Process) callError(op string, err error) error {
	select {
	case <-p.exited:
	case <-time.After(exitWait):
		return fmt.Errorf("plugin %s: %s: %w", p.cfg.Name, op, err)
	}
	return fmt.Errorf("plugin %s: %s: %w", p.cfg.Name, op, ErrExited)
}

// Close stops the plugin (SIGTERM, then SIGKILL after 3s) and removes its
// socket directory. Safe to call more than once.
func (p *Process) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		_ = p.conn.Close()
		p.terminate()
		_ = os.RemoveAll(p.dir)
	})
	return nil
}

func (p *Process) terminate() {
	select {
	case <-p.exited:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.exited:
	case <-time.After(termGrace):
		p.stop()
	}
}
