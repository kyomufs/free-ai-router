package pool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"free-ai-router/internal/provider"
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

	// refMu guards the refresh throttle: an exhausted list asks for a new one, and
	// the gateway's retries of one failed request must not each ask.
	refMu          sync.Mutex
	lastRefreshAsk time.Time
}

// SwitcherConfig describes the switcher.
type SwitcherConfig struct {
	Registry *Registry
	Runner   *Runner
	Core     Core
	Grace    time.Duration
	Log      Logger
	// Classify is the provider's reading of a refusal, used to tell an address that
	// is spent from a tunnel that is broken. Nil means every response counts as
	// success.
	Classify func(resp *http.Response, body []byte) provider.Failure
	// Refresh asks for a new list when the current one is exhausted. Nil means the
	// pool cannot renew itself, and an exhausted list is final.
	//
	// It is called OFF the request path: a refresh takes minutes (a pass plus a
	// probe), and no client should be held for it. The switcher records the request
	// and the schedule answers it.
	Refresh func()
	// Empty describes the exhaustion as the client will read it. Nil falls back to
	// Explain.
	Empty func() string
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
	return observing{inner: s.rt, sw: s, classify: s.cfg.Classify}, nil
}

// observing feeds the outcome of real traffic back into the switcher.
//
// WHY A RESPONSE IS NOT AUTOMATICALLY A SUCCESS. A 2xx answer means the node carried
// the request to the provider and the provider served it: the tunnel did its job, and
// the node stays. A transport error means the tunnel is broken. But a REFUSAL that
// names the address or the free allowance — 429 FreeUsageLimitError, 403
// FreeTierError, the address-level "abusive traffic" flag — is neither: the node is
// working and the provider is still saying no, so keeping it in service hands the
// same refusal to every following request. That is the case this hook exists for, and
// it is read off the provider's own classification rather than its status, because
// one status carries several meanings (see the OpenCode provider's contract).
type observing struct {
	inner http.RoundTripper
	sw    *Switcher
	// classify is the provider's own reading of a refusal, consulted only for
	// non-2xx answers. Nil means every response counts as success — the behaviour a
	// provider without an address budget deserves.
	classify func(resp *http.Response, body []byte) provider.Failure
}

// refusalPeekLimit bounds how much of a refusal body is read to classify it. A
// refusal is small; a body larger than this is not one, and the bytes are put back
// for the caller either way.
const refusalPeekLimit = 64 << 10

func (o observing) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := o.inner.RoundTrip(r)
	if err != nil {
		// A cancelled request is the client's decision, not a broken tunnel.
		if r.Context().Err() == nil {
			o.sw.Suspect("request failed: " + err.Error())
		}
		return nil, err
	}

	if verdict, ok := o.refusal(resp); ok && verdict.AddressRejected {
		// The address is spent, not the tunnel. Marking the whole exit pool is what
		// keeps the next request from spending itself on a neighbour that shares the
		// same allowance.
		o.sw.SuspectAddress(resp.StatusCode, "the provider refuses this exit network")
		return resp, nil
	}

	o.sw.Succeed()
	return resp, nil
}

// refusal classifies a non-2xx answer, restoring the body for the caller.
//
// A refusal body is passed to clients VERBATIM, so the peek must not consume it: the
// bytes read here are put back, and the response the gateway sees is the one the
// provider sent.
func (o observing) refusal(resp *http.Response) (provider.Failure, bool) {
	if o.classify == nil || resp == nil || resp.StatusCode < 300 {
		return provider.Failure{}, false
	}
	if resp.Body == nil {
		return o.classify(resp, nil), true
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, refusalPeekLimit))
	if err != nil {
		// The body could not be read: the connection is the problem, and the caller
		// must not be left with a half-consumed one.
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return provider.Failure{}, false
	}
	resp.Body.Close()
	// Restored, not replaced by the probe: the client parses this body itself.
	resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), resp.Body))
	return o.classify(resp, body), true
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
//
// AN EXHAUSTED LIST IS NOT THE END. When every node has been spent, the failed
// attempt asks for a new list (Refresh) — the addresses were spent when they were
// measured, the free allowance returns by itself, and a fresh pass is the only way
// to learn whether it has. The caller still gets an error, because the request in
// flight cannot be served; what changes is that the pool is now renewing itself
// instead of waiting for the next scheduled slot.
func (s *Switcher) Switch(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("%w: the pool is stopped", ErrNotReady)
	}

	node, ok := s.cfg.Registry.Next()
	if !ok {
		// Nothing left to try: ask for a new list, once, and say so plainly. The
		// request is recorded rather than awaited (see Refresh).
		s.requestRefresh("the list is exhausted")
		return fmt.Errorf("%w: %s", ErrNotReady, s.empty())
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

// requestRefresh asks for a new list outside the request path, at most once per
// refreshGap.
//
// The gap is what keeps a client that is retrying from turning into a burst of
// passes: the gateway repeats a failed request several times, and each repeat would
// otherwise ask for another pass while the first one has not even finished.
func (s *Switcher) requestRefresh(reason string) {
	if s.cfg.Refresh == nil {
		return
	}
	s.refMu.Lock()
	if !s.lastRefreshAsk.IsZero() && time.Since(s.lastRefreshAsk) < refreshGap {
		s.refMu.Unlock()
		return
	}
	s.lastRefreshAsk = time.Now()
	s.refMu.Unlock()

	if s.cfg.Log != nil {
		s.cfg.Log.Warn("pool: %s; asking for a new list", reason)
	}
	s.cfg.Refresh()
}

// empty is the sentence the client reads when the list is exhausted.
//
// It names what was tried and where the attempt stands, because "no server" alone
// reads as a broken proxy. The pool is renewing itself while the client is refused,
// and that is what the message has to say.
func (s *Switcher) empty() string {
	if s.cfg.Empty != nil {
		return s.cfg.Empty()
	}
	if s.cfg.Explain != nil {
		return s.cfg.Explain()
	}
	return "every server of the list was tried and refused"
}

// refreshGap bounds how often an exhausted list asks to be rebuilt.
//
// A minute, not the retry ladder: the refresh runs in the background, and asking
// again while the previous pass is still measuring would start a second pass against
// one quota.
const refreshGap = time.Minute

// SuspectAddress marks the whole exit pool of the serving node as spent.
//
// It is the refusal counterpart of Suspect: the node is alive, the address is the
// problem, and every server in that network shares the allowance. One request asks
// for one switch, exactly as with Suspect — the gateway retries a refused request
// several times, and each retry would otherwise burn another node.
func (s *Switcher) SuspectAddress(status int, reason string) {
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

	spent := s.cfg.Registry.SpendPool(t.Node.ID, t.Node.ExitIP, reason)
	if s.cfg.Log != nil && spent > 1 {
		s.cfg.Log.Warn("pool: exit pool of %s is spent (%d servers quarantined), moving on", t.Node.Label, spent)
	}
	go func() {
		if err := s.Switch(context.Background()); err != nil && s.cfg.Log != nil {
			s.cfg.Log.Warn("pool: switch after refusal (HTTP %d): %v", status, err)
		}
	}()
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
