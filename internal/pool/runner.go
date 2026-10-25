package pool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Tunnel is one running core serving one node on one loopback port.
type Tunnel struct {
	Node  Node
	Port  int
	Proxy *url.URL

	cmd     *exec.Cmd
	stderr  *tailBuffer
	dir     string
	done    chan struct{}
	once    sync.Once
	release func()
}

// Runner starts and stops cores. It knows nothing about how a config is rendered.
//
// It also owns the port range: two cores started in parallel would otherwise pick
// the same port, and the second would fail for a reason that has nothing to do
// with its node.
type Runner struct {
	binary string
	dir    string

	mu    sync.Mutex
	taken map[int]bool
}

// NewRunner takes the binary path explicitly (empty means "look it up on PATH")
// and the directory for per-tunnel config files (empty means the system temp
// directory).
func NewRunner(binary, dir string) *Runner { return &Runner{binary: binary, dir: dir} }

// DefaultPortRange is the range the pool owns for its tunnels. Fixed rather than
// ephemeral so that a stuck core from a previous run can be recognized in a
// process list.
const (
	DefaultPortLow  = 38200
	DefaultPortHigh = 38399
)

// Start launches a core for one node and waits until its port accepts a
// connection. A core that dies or never listens is an error, never a tunnel: the
// caller must be able to keep serving through the previous node.
func (r *Runner) Start(ctx context.Context, c Core, n Node) (*Tunnel, error) {
	if c == nil {
		return nil, errors.New("pool: no core")
	}
	if !c.Supports(n) {
		return nil, fmt.Errorf("pool: core %s cannot render %s", c.ID(), n.Proto)
	}
	port, release, err := r.reserve()
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	cfg, err := c.Config(n, port)
	if err != nil {
		return nil, fmt.Errorf("pool: render %s: %w", n.Proto, err)
	}

	bin := r.binary
	if bin == "" {
		bin, err = exec.LookPath(c.ID())
		if err != nil {
			return nil, fmt.Errorf("pool: core %s not found: %w", c.ID(), err)
		}
	}
	dir, err := os.MkdirTemp(r.dir, "pool-"+c.ID()+"-*")
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	cmd := exec.Command(bin, "-d", dir, "-f", cfgPath)
	stderr := &tailBuffer{limit: 2048}
	cmd.Stderr = stderr
	cmd.Stdout = stderr
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("pool: start %s: %w", c.ID(), err)
	}

	t := &Tunnel{
		Node:    n,
		Port:    port,
		release: release,
		Proxy:   &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", itoa(port))},
		cmd:     cmd,
		stderr:  stderr,
		dir:     dir,
		done:    make(chan struct{}),
	}
	go func() {
		cmd.Wait()
		close(t.done)
	}()

	if err := t.waitReady(ctx, 5*time.Second); err != nil {
		t.Stop(0)
		return nil, err
	}
	keep = true
	return t, nil
}

func (t *Tunnel) waitReady(ctx context.Context, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	addr := net.JoinHostPort("127.0.0.1", itoa(t.Port))
	for {
		select {
		case <-t.done:
			return fmt.Errorf("pool: core exited before it listened: %s", t.stderr.Tail())
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pool: core did not listen on %s within %s: %s", addr, budget, t.stderr.Tail())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Stop ends the core: SIGTERM, then SIGKILL if it outstays the grace period. The
// grace exists so that connections already flowing keep working while the next
// node takes over.
func (t *Tunnel) Stop(grace time.Duration) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		if t.cmd != nil && t.cmd.Process != nil {
			t.cmd.Process.Signal(syscall.SIGTERM)
			wait := grace + 2*time.Second
			if wait < 2*time.Second {
				wait = 2 * time.Second
			}
			select {
			case <-t.done:
			case <-time.After(wait):
				t.cmd.Process.Kill()
				<-t.done
			}
		}
		if t.dir != "" {
			os.RemoveAll(t.dir)
		}
		if t.release != nil {
			t.release()
		}
	})
}

// Alive reports whether the core is still running.
func (t *Tunnel) Alive() bool {
	if t == nil || t.done == nil {
		return false
	}
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

// ExitReason is the core's own last words, used as a quarantine reason.
func (t *Tunnel) ExitReason() string { return t.stderr.Tail() }

type tailBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
	if b.buf.Len() > b.limit {
		b.buf.Next(b.buf.Len() - b.limit)
	}
	return len(p), nil
}

func (b *tailBuffer) Tail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// reserve takes a port that no other core of this runner holds.
//
// The port is held for the life of the tunnel: the listening socket is the core's,
// and the local address is what the round tripper dials, so two tunnels cannot share
// one.
func (r *Runner) reserve() (int, func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.taken == nil {
		r.taken = make(map[int]bool)
	}
	port, err := freePort(DefaultPortLow, DefaultPortHigh, r.taken)
	if err != nil {
		return 0, nil, err
	}
	r.taken[port] = true
	release := func() {
		r.mu.Lock()
		delete(r.taken, port)
		r.mu.Unlock()
	}
	return port, release, nil
}

// FreePort finds a free loopback port in the range and returns it.
//
// Bind-then-release leaves a window in which another process could take the port.
// The alternative — reading the core's own port report — does not exist for a
// fixed mixed-port, and on a host that runs one router the window is harmless.
func FreePort(low, high int) (int, error) { return freePort(low, high, nil) }

func freePort(low, high int, taken map[int]bool) (int, error) {
	for port := low; port <= high; port++ {
		if taken[port] {
			continue
		}
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
		if err != nil {
			continue
		}
		l.Close()
		return port, nil
	}
	return 0, fmt.Errorf("pool: no free port in %d-%d", low, high)
}
