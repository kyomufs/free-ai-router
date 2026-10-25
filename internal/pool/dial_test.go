package pool

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// The pool reaches a core with its own CONNECT, so the exchange is the pool's to
// get right: a proxy that answers anything but 200 must not look like a tunnel, and
// bytes the proxy sends after its headers must not be dropped.
func TestConnectViaSpeaksConnect(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	asked := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		asked <- req.Host
		// The greeting is sent together with the headers on purpose: a proxy is
		// allowed to do that, and the tunnel must keep those bytes.
		fmt.Fprint(c, "HTTP/1.1 200 Connection Established\r\n\r\nGREETING")
		_, _ = io.Copy(io.Discard, br)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := connectVia(ctx, l.Addr().String(), "example.com:443")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	select {
	case host := <-asked:
		if host != "example.com:443" {
			t.Fatalf("the proxy was asked for %q", host)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the proxy was never asked")
	}

	buf := make([]byte, len("GREETING"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read after the headers: %v", err)
	}
	if string(buf) != "GREETING" {
		t.Fatalf("the bytes sent with the headers were lost: %q", buf)
	}
}

func TestConnectViaReportsARefusal(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		br := bufio.NewReader(c)
		if _, err := http.ReadRequest(br); err != nil {
			return
		}
		fmt.Fprint(c, "HTTP/1.1 403 Forbidden\r\n\r\n")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := connectVia(ctx, l.Addr().String(), "example.com:443"); err == nil {
		t.Fatal("a refusal must not become a tunnel")
	}
}
