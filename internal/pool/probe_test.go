package pool

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestScanTCPKeepsOnlyLiveEndpoints(t *testing.T) {
	live1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer live1.Close()
	live2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer live2.Close()

	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.Addr().(*net.TCPAddr)
	dead.Close()

	hostPort := func(l net.Listener) (string, int) {
		a := l.Addr().(*net.TCPAddr)
		return a.IP.String(), a.Port
	}
	h1, p1 := hostPort(live1)
	h2, p2 := hostPort(live2)

	nodes := []Node{
		{Host: h1, Port: p1},
		{Host: h2, Port: p2},
		{Host: deadAddr.IP.String(), Port: deadAddr.Port},
	}
	alive := ScanTCP(context.Background(), nodes, 4, time.Second)
	if len(alive) != 2 {
		t.Fatalf("alive %d, want 2", len(alive))
	}
	for _, n := range alive {
		if n.TCPRTT <= 0 {
			t.Fatalf("rtt not recorded: %+v", n)
		}
	}
	if alive[0].TCPRTT > alive[1].TCPRTT {
		t.Fatal("result is not sorted by rtt")
	}
}
