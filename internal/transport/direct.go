package transport

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// direct implements the host's direct-routing transport.
//
// THIS IS NOT a "default stub", but part of the job. An agent lives for a long
// time, and its turn consists of many requests; without connection reuse every
// turn pays a fresh TCP and TLS handshake. That is why the transport keeps a
// warm connection pool — it is exactly what the gateway exists as a separate
// process for, rather than as a library inside the agent.
type direct struct {
	rt *http.Transport
}

// DirectConfig are the settings of the direct transport.
type DirectConfig struct {
	// Family pins the address family: "auto", "v4" or "v6".
	//
	// The point is not performance: some providers tie the limit to the address
	// string, and different families can land in different buckets. Pinning
	// makes the family choice explicit.
	Family string
}

// The pool numbers are chosen for the gateway's load profile: many consecutive
// requests from one client to one host.
const (
	maxIdleConns        = 128
	maxIdleConnsPerHost = 64
	idleConnTimeout     = 90 * time.Second
	tlsHandshakeTimeout = 15 * time.Second
	dialTimeout         = 20 * time.Second
	keepAliveInterval   = 30 * time.Second
)

// NewDirect creates the direct transport.
func NewDirect(cfg map[string]any) (Transport, error) {
	var parsed DirectConfig
	if raw, ok := cfg["family"]; ok {
		if s, ok := raw.(string); ok {
			parsed.Family = s
		}
	}
	return &direct{rt: buildDirectTransport(parsed.Family)}, nil
}

// buildDirectTransport assembles an http.Transport with a warm pool.
func buildDirectTransport(family string) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAliveInterval,
	}

	// A family is given: we take the dial into our own hands and substitute the
	// network name. A family error becomes an ordinary connection error — that
	// is how the caller will see it, and no separate path is needed for it.
	dial := dialer.DialContext
	if network := networkFor(family); network != "" {
		dial = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		}
	}

	return &http.Transport{
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// networkFor turns the family setting into a network name.
//
// An empty string means "let the system decide": that is how "auto" and any
// unrecognised value work. An unknown value is not an error — it means exactly
// the same as "auto".
func networkFor(family string) string {
	switch family {
	case "v4":
		return "tcp4"
	case "v6":
		return "tcp6"
	default:
		return ""
	}
}

// ID implements Transport.
func (d *direct) ID() ID { return Direct }

// RoundTripper implements Transport.
func (d *direct) RoundTripper() (http.RoundTripper, error) { return d.rt, nil }

// Start implements Transport. The direct transport has nothing to bring up.
func (d *direct) Start(context.Context) error { return nil }

// Stop implements Transport.
func (d *direct) Stop() error {
	d.rt.CloseIdleConnections()
	return nil
}

// Info implements Transport.
func (d *direct) Info() Info {
	return Info{
		ID:      Direct,
		Summary: "direct host routing with a warm connection pool",
	}
}

// Pinned returns a transport bound to one address family.
//
// WHY IT IS EXPORTED. Something outside the request path has to ask "which
// address does my traffic leave from", and the answer is only true if it is
// asked over the same transport and the same family as the requests themselves.
// Asking over the system's own choice would answer a different question, and the
// difference is exactly what the answer is wanted for.
//
// The caller owns the returned pool and must close its idle connections.
func Pinned(family string) http.RoundTripper { return buildDirectTransport(family) }

// NewRegistryWithDefaults creates a registry with all built-in transports.
//
// The function exists so that the entry point does not assemble the registry by
// hand: a new transport is registered in one place, and no caller forgets to
// add it.
func NewRegistryWithDefaults() *Registry {
	r := NewRegistry()
	r.Register(Info{
		ID:      Direct,
		Summary: "direct host routing with a warm connection pool",
	}, NewDirect)
	return r
}
