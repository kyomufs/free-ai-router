package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"free-ai-router/internal/provider"
)

// The budgets of one attempt at the provider.
//
// WHY A WATCHDOG AND NOT A CLIENT TIMEOUT. Every timeout the standard library
// offers covers a stage of its own: the dial, the TLS handshake, the time a
// connection may sit idle in the pool. None of them covers the one case that
// matters here — a connection that is established and then goes quiet. A route
// that changed, an uplink that was switched, a VPN server that went away: no
// FIN, no RST, nothing to read and nothing that fails. A request waiting on such
// a connection waits for as long as anyone is willing to wait, and the client
// waits with it. That is what an operator reads as "the daemon has hung".
//
// The watchdog closes the hole: the attempt's context is cancelled the moment
// the provider has stayed silent longer than the budget, and the transport tears
// the connection down.
//
// TWO STAGES, because a stream has two kinds of silence. Before the headers
// there is no evidence of anything and the budget is short. Between events a
// model that reasons may hold a pause of minutes while still working, and a
// short budget there would tear a healthy stream.
const (
	// defaultFirstEventTimeout and defaultIdleTimeout are the fallbacks for a
	// provider that declares no budget of its own. The daemon passes the
	// configured values in; these stay equal to the configuration defaults, and
	// a test holds them together.
	defaultFirstEventTimeout = 30 * time.Second
	defaultIdleTimeout       = 120 * time.Second

	// transportAttempts is how many times one request is sent when the failure
	// happened on the way to the provider rather than in the answer.
	//
	// A pooled connection can be dead without anyone noticing: the peer is gone,
	// no FIN arrived, and the first write after that reads as an EOF instead of
	// an answer. One attempt turns such a blip into a failed turn; a retry on a
	// fresh connection turns it into a slow turn.
	transportAttempts = 3

	// silenceAttempts is how many times a stream is sent when its headers never
	// arrived.
	//
	// One retry, no more, and only for that stage. A stream whose headers never
	// came is the same dead connection seen a moment later, and a fresh one
	// usually answers at once. A request that does not stream is not repeated
	// here at all: its silence budget is measured in minutes, and the provider
	// had all of it to answer.
	silenceAttempts = 2

	// modelFallbacks is how many OTHER models one request may be moved to when
	// the model it asked for is unavailable upstream.
	//
	// Two, not the whole catalog: each move is a fresh attempt on a fresh
	// connection, and the client waits through all of them. Two is enough to
	// survive one dead endpoint — measured, the free lane had several models out
	// at once — while keeping the worst case a few seconds rather than a minute.
	modelFallbacks = 2
)

// transportDelays are the pauses before the second and the following attempts.
//
// Short on purpose: the fault being absorbed is a connection dropped by the
// kernel or a packet lost on the way, not a provider that is down. Waiting
// longer here would only delay the answer the client is entitled to.
var transportDelays = []time.Duration{200 * time.Millisecond, 700 * time.Millisecond}

// Causes explaining why an attempt was cut short.
var (
	// errFirstEvent — the headers did not arrive within the first stage.
	errFirstEvent = errors.New("the first event did not arrive within the budget")
	// errSilence — a response that had started stopped arriving for longer than
	// the silence budget.
	errSilence = errors.New("the response stopped arriving within the budget")
	// errNoAnswer — a request that does not stream got no answer within the
	// budget.
	errNoAnswer = errors.New("the answer did not arrive within the budget")
)

// watchdog enforces the budget of one attempt.
//
// It owns the attempt's context, because cancelling that context is the only way
// to make a read on a quiet connection return. The budget is re-armed before
// every read: the silence being bounded is the one between events, and an
// attempt that has already produced something is not the same as one that has
// produced nothing.
type watchdog struct {
	mu     sync.Mutex
	cancel context.CancelCauseFunc
	timer  *time.Timer
	cause  error
	// gen is the number of the budget currently running.
	//
	// A timer that has already elapsed cannot be stopped: Stop reports it, but
	// the callback is on its way, and a callback of an old budget cancelling an
	// attempt that has just received data would be a fault of its own. The
	// callback therefore carries the number of the budget it was armed for, and
	// only the current one counts.
	gen int
}

// newWatchdog opens an attempt and returns its context.
func newWatchdog(parent context.Context) (*watchdog, context.Context) {
	ctx, cancel := context.WithCancelCause(parent)
	return &watchdog{cancel: cancel}, ctx
}

// arm starts the budget, replacing any budget already running.
//
// A budget of zero disarms the watchdog: a stage the operator switched off does
// not exist. The first cause wins — the stage that was running when the budget
// elapsed is the one that explains the attempt.
func (w *watchdog) arm(budget time.Duration, cause error) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.gen++
	if budget <= 0 || w.cause != nil {
		return
	}
	gen := w.gen
	w.timer = time.AfterFunc(budget, func() { w.fire(gen, cause) })
}

// fire cancels the attempt with the given cause.
func (w *watchdog) fire(gen int, cause error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if gen != w.gen || w.cause != nil {
		return
	}
	w.cause = cause
	w.cancel(cause)
}

// fired reports the cause of the cancellation, or nil when the watchdog was not
// the one who ended the attempt. It is what tells a provider that went quiet
// from a client that hung up.
func (w *watchdog) fired() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cause
}

// stop releases the attempt and its timer.
func (w *watchdog) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.cancel(nil)
}

// retriableTransport reports whether a failed attempt is worth repeating on a
// fresh connection.
//
// A failure is worth repeating when it happened on the way to the provider: a
// pooled connection that was already dead, a route that was gone when it was
// used, a certificate that did not verify because the middle of the path was
// being replaced at that moment. A refusal is NOT repeated here — that is the
// provider's answer, and what to do with it is the retry policy's business, not
// the transport's.
func retriableTransport(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Someone else ended the attempt: the client hung up, or our own
		// watchdog fired. Repeating it would work against that decision.
		return false
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	// Every dial and every read of the connection reports through net.OpError.
	//
	// A certificate that did not verify arrives through the same path and IS
	// repeated, because the evidence says the verdict is as unstable as the path:
	// over an uplink that is being switched, one request in two fails the
	// handshake and the next one succeeds against the very same certificate.
	// Treating it as final turns that flakiness into a failed turn, and the
	// request body is never sent on a handshake that failed, so repeating it
	// discloses nothing. What is left is the cost of two failed handshakes
	// against a certificate that is wrong for a reason — and that is cheaper than
	// a turn lost to one that is not.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Err != nil
	}

	for _, target := range []error{
		syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE,
		syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.ETIMEDOUT,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// idleCloser is what a transport implements when it holds a pool of connections.
type idleCloser interface {
	CloseIdleConnections()
}

// dropConnections empties the provider's pool of idle connections.
//
// Called before a retry, and only then: the attempt that failed is the reason
// the pool is suspect, and reaching the provider over a fresh connection is the
// whole point of repeating it.
func (h *Handler) dropConnections(p provider.Provider) {
	if local, ok := p.(provider.Local); ok {
		if rt := local.RoundTripper(); rt != nil {
			if c, ok := rt.(idleCloser); ok {
				c.CloseIdleConnections()
			}
			return
		}
	}
	if h.Transport == nil {
		return
	}
	if rt, err := h.Transport.RoundTripper(); err == nil && rt != nil {
		if c, ok := rt.(idleCloser); ok {
			c.CloseIdleConnections()
		}
	}
}
