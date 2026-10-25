package pool

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakeNodes() []Node {
	return []Node{
		{ID: "a", Label: "a", Proto: "vless", Host: "h", Port: 443, Raw: "vless://a", ProbeRTT: 100 * time.Millisecond, ExitIP: "1.1.1.1"},
		{ID: "b", Label: "b", Proto: "vless", Host: "h", Port: 443, Raw: "vless://b", ProbeRTT: 200 * time.Millisecond, ExitIP: "2.2.2.2"},
	}
}

func eventually(t *testing.T, budget time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition was not met in time")
}

func TestSwitcherServesAndMovesOnFailure(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	reg := NewRegistry(10, 1)
	reg.Replace(fakeNodes())
	sw := NewSwitcher(SwitcherConfig{Registry: reg, Runner: NewRunner(exe, t.TempDir()), Core: fakeCoreImpl{}, Grace: 0})
	defer sw.Stop()

	if err := sw.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, ok := sw.Active()
	if !ok {
		t.Fatal("no active node after ensure")
	}

	// The round tripper must send through the tunnel that is serving. A request is
	// the only honest check here: the transport has no proxy function to inspect,
	// because the tunnel is reached by the dialer.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "through the tunnel")
	}))
	defer target.Close()

	rt, err := sw.RoundTripper()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: rt}).Get(target.URL)
	if err != nil {
		t.Fatalf("request through the tunnel: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "through the tunnel" {
		t.Fatalf("answer through the tunnel: %s %q", resp.Status, body)
	}
	tunnel := sw.cur.Load()
	if tunnel == nil {
		t.Fatal("no tunnel after a served request")
	}
	connects := tunnelConnects(t, tunnel.dir)
	want := strings.TrimPrefix(target.URL, "http://")
	if len(connects) == 0 || connects[len(connects)-1] != want {
		t.Fatalf("the tunnel was asked to reach %v, the request went to %s", connects, want)
	}

	if !sw.HealthCheck(context.Background()) {
		t.Fatal("a live tunnel must pass the health check")
	}

	// One failure is enough here: the threshold is 1.
	sw.Suspect("test")
	eventually(t, 10*time.Second, func() bool {
		active, ok := sw.Active()
		return ok && active.ID != first.ID
	})
	if switches, _ := sw.Stats(); switches < 2 {
		t.Fatalf("switches %d", switches)
	}
}

// An address that dies while it is serving shows up as a request that cannot be
// completed. Nothing else in the pool looks at the far end, so a failed request must
// be enough to move on.
func TestSwitcherMovesOnAFailedRequest(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	reg := NewRegistry(10, 2)
	reg.Replace(fakeNodes())
	sw := NewSwitcher(SwitcherConfig{Registry: reg, Runner: NewRunner(exe, t.TempDir()), Core: fakeCoreImpl{}, Grace: 0})
	defer sw.Stop()
	if err := sw.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, _ := sw.Active()

	rt, err := sw.RoundTripper()
	if err != nil {
		t.Fatal(err)
	}
	// Port 1 is not listening anywhere: the tunnel answers the CONNECT with a
	// gateway error, which is the shape a server that stopped forwarding has.
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}
	if resp, err := client.Get("http://127.0.0.1:1/"); err == nil {
		resp.Body.Close()
		t.Fatal("a closed target must not produce a response")
	}

	eventually(t, 10*time.Second, func() bool {
		active, ok := sw.Active()
		return ok && active.ID != first.ID
	})
}

func TestSwitcherReportsNotReadyWithoutAList(t *testing.T) {
	sw := NewSwitcher(SwitcherConfig{Registry: NewRegistry(10, 1), Runner: NewRunner("/nonexistent", ""), Core: fakeCoreImpl{}})
	defer sw.Stop()

	err := sw.Ensure(context.Background())
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("ensure error %v", err)
	}

	rt, err := sw.RoundTripper()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://provider.example/", nil)
	if _, err := rt.RoundTrip(req); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a request must name the condition: %v", err)
	}

	// The gateway retries through a fresh connection by asking the transport to
	// drop its idle ones. The wrapper must not hide that capability.
	c, ok := rt.(interface{ CloseIdleConnections() })
	if !ok {
		t.Fatalf("%T does not let the gateway drop idle connections", rt)
	}
	c.CloseIdleConnections()
}

// The provider admits only the reference client's TLS. A pool that reached the
// tunnel with a Go handshake would be answered like a spent address, and every
// server would be rejected for a reason that belongs to the transport. This is the
// guard for exactly that: the hello that leaves the tunnel must be the captured one.
func TestSwitcherPresentsTheProviderClientThroughTheTunnel(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	var (
		mu     sync.Mutex
		hellos [][]uint16
	)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.TLS = &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			mu.Lock()
			hellos = append(hellos, append([]uint16(nil), hello.CipherSuites...))
			mu.Unlock()
			return nil, nil
		},
	}
	srv.StartTLS()
	defer srv.Close()

	reg := NewRegistry(10, 1)
	reg.Replace(fakeNodes())
	sw := NewSwitcher(SwitcherConfig{Registry: reg, Runner: NewRunner(exe, t.TempDir()), Core: fakeCoreImpl{}, Grace: 0})
	defer sw.Stop()
	if err := sw.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	rt, err := sw.RoundTripper()
	if err != nil {
		t.Fatal(err)
	}

	// The test server's certificate is self-signed, so the request itself must
	// fail: the handshake is what this test is about, and the hello has already
	// been sent by the time the certificate is checked.
	client := &http.Client{Transport: rt, Timeout: 10 * time.Second}
	if resp, err := client.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("a self-signed server cannot be verified, the request should have failed")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hellos) != 1 {
		t.Fatalf("the tunnel carried %d handshakes, want one", len(hellos))
	}
	// Go's own list opens 1301,1302,1303,c02a; the captured reference list opens
	// 1301,1302,1303,c02b. The difference is the whole point of the hello.
	want := []uint16{0x1301, 0x1302, 0x1303, 0xc02b}
	if len(hellos[0]) < len(want) || !slices.Equal(hellos[0][:len(want)], want) {
		t.Fatalf("the tunnel sent the cipher suites %04x, want them to start with %04x", hellos[0], want)
	}
}

// A candidate whose core cannot start must not replace a working tunnel.
func TestSwitcherKeepsTheOldTunnelWhenACandidateFails(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	reg := NewRegistry(10, 1)
	reg.Replace(fakeNodes())
	sw := NewSwitcher(SwitcherConfig{Registry: reg, Runner: NewRunner(exe, t.TempDir()), Core: fakeCoreImpl{}, Grace: 0})
	defer sw.Stop()

	if err := sw.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	first, _ := sw.Active()

	// From now on every core dies at once.
	t.Setenv("POOL_TEST_FAKE_CORE_FAIL", "1")
	if err := sw.Switch(context.Background()); err == nil {
		t.Fatal("a dying core must not produce a tunnel")
	}
	if active, ok := sw.Active(); !ok || active.ID != first.ID {
		t.Fatalf("the serving node changed to %+v", active)
	}
	if rt, _ := sw.RoundTripper(); rt == nil {
		t.Fatal("the round tripper disappeared")
	}
}

// A switch that arrives after the switcher has stopped must not start a core.
//
// The failure it prevents is silent: the core starts, binds a port, serves nothing,
// and outlives the daemon, because Stop has already stopped the tunnel it knew
// about and the next Stop never comes.
func TestSwitcherRefusesToSwitchAfterStop(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	reg := NewRegistry(10, 1)
	reg.Replace(fakeNodes())
	sw := NewSwitcher(SwitcherConfig{Registry: reg, Runner: NewRunner(exe, t.TempDir()), Core: fakeCoreImpl{}, Grace: 0})

	if err := sw.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	sw.Stop()

	err = sw.Switch(context.Background())
	if err == nil {
		t.Fatal("a stopped switcher started a core")
	}
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("error %v, want ErrNotReady", err)
	}
	if _, ok := sw.Active(); ok {
		t.Fatal("a stopped switcher still names a serving node")
	}
	sw.Stop() // idempotent
}
