package transport

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
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

// Dialer is a plain TCP dialer. It is what a client transport is built on when the
// connection is made by someone else — by a tunnel, for instance.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// buildDirectTransport assembles an http.Transport with a warm pool.
func buildDirectTransport(family string) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: keepAliveInterval,
	}

	// A family is given: we take the dial into our own hands and substitute the
	// network name. A family error becomes an ordinary connection error — that
	// is how the caller will see it, and no separate path is needed for it.
	dial := Dialer(dialer.DialContext)
	if network := networkFor(family); network != "" {
		dial = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		}
	}

	return ClientTransport(dial)
}

// ClientTransport returns the HTTP transport the provider admits, over dial.
//
// WHY IT IS EXPORTED, and not spelled out twice. The provider decides whether a
// request may use the free lane by the TLS client, so the transport IS part of the
// wire contract (see bunClientHello). Anything that sends provider traffic from a
// different place — the pool, which sends it through a server — must present the
// same client, or it is refused for a reason that has nothing to do with the exit
// address and every measurement it makes is worthless.
//
// The caller owns the returned pool and must close its idle connections.
func ClientTransport(dial Dialer) *http.Transport {
	// The TLS client is part of the request here, not an implementation detail.
	//
	// MEASURED on 2026-10-24 against the inference endpoint: the same method,
	// path, headers and body were answered 200 by the reference client and 429
	// (FreeUsageLimitError) by Go's crypto/tls, by curl and by a uTLS browser
	// profile. Replaying the reference client's captured hello kept the 200.
	// The service therefore looks at the TLS fingerprint, and a plain Go
	// transport cannot pass no matter how the headers are set.
	//
	// The hello is BoringSSL's default one, not a browser's: a browser profile
	// was refused. HTTP/1.1 and ALPN "http/1.1" are what that client sends; the
	// protocol alone is not the discriminator, but keeping it identical removes
	// a second difference.
	dialTLS := func(ctx context.Context, _, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		conn, err := dial(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}

		// DialTLSContext is called by the transport instead of its own TLS
		// handshake, so TLSHandshakeTimeout does not apply. The deadline is set
		// here and cleared once the handshake is done; the idle pool needs the
		// connection to be free of it.
		deadline := time.Now().Add(tlsHandshakeTimeout)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = conn.SetDeadline(deadline)

		uc := utls.UClient(conn, &utls.Config{
			ServerName: host,
			MinVersion: utls.VersionTLS12,
			MaxVersion: utls.VersionTLS13,
			NextProtos: []string{"http/1.1"},
		}, utls.HelloCustom)
		if err := uc.ApplyPreset(bunClientHello(host)); err != nil {
			_ = conn.Close()
			return nil, err
		}
		if err := uc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		_ = conn.SetDeadline(time.Time{})
		return uc, nil
	}

	return &http.Transport{
		DialContext:           dial,
		DialTLSContext:        dialTLS,
		ForceAttemptHTTP2:     false,
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
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
