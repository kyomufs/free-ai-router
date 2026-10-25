package pool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"free-ai-router/internal/transport"
)

// ErrNotReady is the pool's name for the shared transport condition.
var ErrNotReady = transport.ErrNotReady

// Switcher owns the serving tunnel and the round tripper that points at it.
//
// Switching works through one long-lived transport whose dialer reads the tunnel of
// the moment: a swap closes the idle connections, so the next request opens a
// connection to the new server while the old sockets age out or finish in flight.
type Switcher struct {
	cfg SwitcherConfig
	cur atomic.Pointer[Tunnel]
	rt  *http.Transport

	mu       sync.Mutex
	switches int
	failed   int

	// closed keeps a switch from starting a core after the switcher has been
	// stopped: such a core would have nobody to stop it, and the process would
	// outlive the daemon. stopCh ends the grace period early for the same reason.
	closed bool
	stopCh chan struct{}
	wg     sync.WaitGroup

	// susMu guards the one-tunnel-one-switch rule. It is separate from mu on
	// purpose: mu is held across a core start, and a request completing behind a
	// switch must not wait for it.
	susMu     sync.Mutex
	suspected *Tunnel
}

// SwitcherConfig describes the switcher.
type SwitcherConfig struct {
	Registry *Registry
	Runner   *Runner
	Core     Core
	Grace    time.Duration
	Log      Logger
	// Explain describes why nothing can be served, for the error message.
	Explain func() string
}

// NewSwitcher builds the switcher and its round tripper.
func NewSwitcher(cfg SwitcherConfig) *Switcher {
	s := &Switcher{cfg: cfg, stopCh: make(chan struct{})}
	// The transport is the provider's own client, not a generic proxy transport:
	// the lane admits only the reference client's TLS, and a Go handshake through
	// the same server is answered as if the address were spent. The tunnel is
	// reached by a dialer instead of a Proxy function for the same reason — see
	// connectDialer.
	//
	// Rotation still reaches the request: the dialer reads the current tunnel, and
	// a swap closes the idle connections, so nothing new goes through the old one.
	s.rt = transport.ClientTransport(connectDialer(func() (string, error) {
		t := s.cur.Load()
		if t == nil || t.Proxy == nil {
			return "", fmt.Errorf("%w: %s", ErrNotReady, s.explain())
		}
		return t.Proxy.Host, nil
	}))
	return s
}

// RoundTripper implements transport.Transport.
//
// The round tripper reports what the tunnel did, because a request is the only place
// where an address that went dark during service shows up: the health loop sees the
// core, not the far end.
func (s *Switcher) RoundTripper() (http.RoundTripper, error) {
	return observing{inner: s.rt, sw: s}, nil
}

// observing feeds the outcome of real traffic back into the switcher.
//
// WHY ONLY TRANSPORT ERRORS. A response, whatever its status, is the provider's
// answer about the request: it means the node carried the request there and the
// answer came back, which is all this transport promises. A broken connection is the
// tunnel failing at its own job, and moving to the next node is the one thing the
// pool can do about it.
type observing struct {
	inner http.RoundTripper
	sw    *Switcher
}

func (o observing) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := o.inner.RoundTrip(r)
	if err != nil {
		// A cancelled request is the client's decision, not a broken tunnel.
		if r.Context().Err() == nil {
			o.sw.Suspect("request failed: " + err.Error())
		}
		return nil, err
	}
	o.sw.Succeed()
	return resp, nil
}

// CloseIdleConnections keeps the wrapper transparent to the gateway, which asks
// the transport for a fresh connection before repeating a failed attempt.
func (o observing) CloseIdleConnections() {
	if c, ok := o.inner.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// Ensure brings up a tunnel if none is serving.
func (s *Switcher) Ensure(ctx context.Context) error {
	if t := s.cur.Load(); t != nil && t.Alive() {
		return nil
	}
	return s.Switch(ctx)
}

// Switch moves to the next node of the list.
//
// The order is deliberate: start the new core, swap the pointer, drop idle
// connections, and only then stop the old core after a grace period. A failed start
// leaves the pointer alone, so a broken candidate cannot take the service down.
func (s *Switcher) Switch(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("%w: the pool is stopped", ErrNotReady)
	}

	node, ok := s.cfg.Registry.Next()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotReady, s.explain())
	}

	t, err := s.cfg.Runner.Start(ctx, s.cfg.Core, node)
	if err != nil {
		// The node could not even raise a core: count it and try the next one on
		// the following call rather than blocking the request path here.
		s.cfg.Registry.Suspect(node.ID, "core did not start: "+err.Error())
		s.failed++
		return fmt.Errorf("pool: %s did not start: %w", node.Label, err)
	}

	old := s.cur.Swap(t)
	s.rt.CloseIdleConnections()
	s.switches++
	if s.cfg.Log != nil {
		s.cfg.Log.Warn("pool: serving through %s (%s)", node.Label, node.ExitIP)
	}
	if old != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			// A live request may still be using the old tunnel; a graceful stop
			// gives it the grace period to finish.
			select {
			case <-s.stopCh:
			case <-time.After(s.grace()):
			}
			old.Stop(time.Second)
		}()
	}
	return nil
}

// Suspect marks the serving node as failing and moves on.
//
// One tunnel asks for one switch. A dead tunnel fails several requests in a row —
// the gateway alone retries three times — and each of them would otherwise start
// another core and burn a node of the list.
func (s *Switcher) Suspect(reason string) {
	t := s.cur.Load()
	if t == nil {
		return
	}
	s.susMu.Lock()
	if s.suspected == t {
		s.susMu.Unlock()
		return
	}
	s.suspected = t
	s.susMu.Unlock()

	s.cfg.Registry.Suspect(t.Node.ID, reason)
	go func() {
		if err := s.Switch(context.Background()); err != nil && s.cfg.Log != nil {
			s.cfg.Log.Warn("pool: switch: %v", err)
		}
	}()
}

// Succeed clears the failure count of the serving node.
//
// It also forgets that this tunnel already asked for a switch: a tunnel that
// answered afterwards is not a tunnel to move away from, and a later failure must be
// able to ask again.
func (s *Switcher) Succeed() {
	t := s.cur.Load()
	if t == nil {
		return
	}
	s.cfg.Registry.Succeed(t.Node.ID)
	s.susMu.Lock()
	if s.suspected == t {
		s.suspected = nil
	}
	s.susMu.Unlock()
}

// HealthCheck verifies that the serving core is alive and still listening.
//
// A tunnel can die without any request being in flight — the core exits, the process
// is killed, the machine runs out of file descriptors — and the next request would
// then fail for a reason the pool already knows about.
func (s *Switcher) HealthCheck(ctx context.Context) bool {
	t := s.cur.Load()
	if t == nil {
		if err := s.Ensure(ctx); err != nil {
			return false
		}
		return true
	}
	if !t.Alive() {
		s.Suspect("core exited: " + t.ExitReason())
		return false
	}
	conn, err := net.DialTimeout("tcp", t.Proxy.Host, time.Second)
	if err != nil {
		s.Suspect("tunnel stopped listening: " + err.Error())
		return false
	}
	_ = conn.Close()
	s.Succeed()
	return true
}

// Active reports the serving node.
func (s *Switcher) Active() (Node, bool) {
	t := s.cur.Load()
	if t == nil {
		return Node{}, false
	}
	return t.Node, true
}

// Stop shuts the serving tunnel down.
//
// It also closes the door behind itself: a switch that arrives afterwards would
// start a core with nobody to stop it. Tunnels still in their grace period are
// ended now instead of being left to the sleep.
func (s *Switcher) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.stopCh)
	t := s.cur.Swap(nil)
	s.mu.Unlock()

	if t != nil {
		t.Stop(time.Second)
	}
	s.rt.CloseIdleConnections()
	s.wg.Wait()
}

// Stats reports the switch counters for the interfaces.
func (s *Switcher) Stats() (switches, failed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.switches, s.failed
}

func (s *Switcher) grace() time.Duration {
	if s.cfg.Grace <= 0 {
		return 30 * time.Second
	}
	return s.cfg.Grace
}

func (s *Switcher) explain() string {
	if s.cfg.Explain != nil {
		return s.cfg.Explain()
	}
	return "no server is ready"
}

// ErrNoNodes is returned when the pool has no candidate at all.
var ErrNoNodes = errors.New("pool: no server in the relevant list")
