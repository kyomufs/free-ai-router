package pool

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"free-ai-router/internal/transport"
)

// The budget for reaching the tunnel and opening the CONNECT. A core that is up but
// wedged, or a proxy port that accepts and then says nothing, must fail like a
// network error rather than hold a probe or a client request.
const (
	connectTimeout = 10 * time.Second
	// dialKeepAlive matches the direct transport: the same idle sockets, the same
	// keepalive behaviour, so a tunnel is not penalised for how it is dialled.
	dialKeepAlive = 30 * time.Second
)

// connectDialer returns a dialer that reaches every address through the HTTP proxy
// port of a core.
//
// WHY THE CONNECT IS OURS. net/http would do it for us if the transport had a Proxy
// function — but with Proxy set, a custom TLS dialer is handed the PROXY's address
// and the CONNECT step is skipped entirely (net/http's dialConn returns before it).
// The provider admits only the reference client's TLS (see transport.ClientTransport),
// so the handshake has to be ours, and that forces the CONNECT to be ours too.
//
// proxyAddr is read per dial, not captured: that is how rotation reaches a request
// that has not opened its connection yet.
func connectDialer(proxyAddr func() (string, error)) transport.Dialer {
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		via, err := proxyAddr()
		if err != nil {
			return nil, err
		}
		return connectVia(ctx, via, addr)
	}
}

// connectVia opens addr through the HTTP proxy at via.
func connectVia(ctx context.Context, via, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: dialKeepAlive}
	conn, err := dialer.DialContext(ctx, "tcp", via)
	if err != nil {
		return nil, fmt.Errorf("pool: proxy %s: %w", via, err)
	}

	deadline := time.Now().Add(connectTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, addr); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("pool: proxy %s: %w", via, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("pool: proxy %s: %w", via, err)
	}
	// The body of a 200 CONNECT is the tunnel itself, and it is read from br, not
	// from resp.Body; the response is only inspected here.
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("pool: proxy %s refused %s: %s", via, addr, resp.Status)
	}

	_ = conn.SetDeadline(time.Time{})
	// A proxy may answer with headers and, in the same read, with the first bytes of
	// the tunnel. Dropping them would truncate the stream, so they are kept.
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufferedConn serves the bytes the proxy sent after its response headers.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
