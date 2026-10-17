package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The egress resolver: what it reports, and what it keeps when it cannot.
//
// THE POINT OF THESE CHECKS. The address is only bookkeeping, but it is
// bookkeeping that decides which counter a refusal lands in. The two ways of
// getting it wrong are opposite: reporting an address that was never observed
// (a monitor that lies) and forgetting an address that was (a monitor that
// blanks on one dropped packet). Both are covered below.

// echo is a stand-in for the address echo service.
type echo struct {
	mu       sync.Mutex
	body     string
	status   int
	requests []string
}

func (e *echo) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		e.requests = append(e.requests, r.URL.Path)
		body, status := e.body, e.status
		e.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

func (e *echo) set(body string, status int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.body, e.status = body, status
}

// families returns the families the resolver asked for, in order.
func (e *echo) asked() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.requests...)
}

// recordingTransport hands back the server's client for every family and records
// which family was asked.
type recordingTransport struct {
	inner    http.RoundTripper
	families []string
}

func (r *recordingTransport) fn(family string) (http.RoundTripper, error) {
	r.families = append(r.families, family)
	return r.inner, nil
}

// TestTheAddressIsTheOneTheEchoReports — the ordinary path.
func TestTheAddressIsTheOneTheEchoReports(t *testing.T) {
	e := &echo{body: "fl=1f2\nip=203.0.113.7\nts=1.0\n"}
	srv := httptest.NewServer(e.handler())
	defer srv.Close()

	rt := &recordingTransport{inner: srv.Client().Transport}
	r := New(Config{
		Families:  FamiliesFor("v4"),
		URL:       srv.URL + "/cdn-cgi/trace",
		Transport: rt.fn,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if got := r.Address(); got != "203.0.113.7" {
		t.Fatalf("Address = %q, want the reported address", got)
	}

	addresses := r.Addresses()
	if len(addresses) != 1 || addresses[0].Family != FamilyV4 {
		t.Fatalf("Addresses = %+v, want one v4 entry", addresses)
	}
	if addresses[0].Address != "203.0.113.7" || addresses[0].ResolvedAt == 0 {
		t.Fatalf("entry = %+v, want the address and the moment it was seen", addresses[0])
	}
	if addresses[0].Error != "" {
		t.Fatalf("entry carries an error: %s", addresses[0].Error)
	}
	if len(rt.families) != 1 || rt.families[0] != FamilyV4 {
		t.Fatalf("families asked = %v, want just v4", rt.families)
	}
}

// TestBothFamiliesAreAskedWhenTheAxisIsAuto — the addressing axis decides how
// many observations are made.
func TestBothFamiliesAreAskedWhenTheAxisIsAuto(t *testing.T) {
	e := &echo{body: "ip=198.51.100.9\n"}
	srv := httptest.NewServer(e.handler())
	defer srv.Close()

	rt := &recordingTransport{inner: srv.Client().Transport}
	r := New(Config{
		Families:  FamiliesFor("auto"),
		URL:       srv.URL,
		Transport: rt.fn,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if len(rt.families) != 2 || rt.families[0] != FamilyV4 || rt.families[1] != FamilyV6 {
		t.Fatalf("families asked = %v, want v4 then v6", rt.families)
	}
	if addresses := r.Addresses(); len(addresses) != 2 {
		t.Fatalf("Addresses = %+v, want both families", addresses)
	}
}

// TestAFailedObservationKeepsTheLastAddress — a dropped packet does not erase
// what was already observed.
//
// The address changes when the uplink reconnects, which is exactly when an
// observation is most likely to fail. Blanking the monitor at that moment would
// hide the requests that caused the reconnect.
func TestAFailedObservationKeepsTheLastAddress(t *testing.T) {
	e := &echo{body: "ip=203.0.113.7\n"}
	srv := httptest.NewServer(e.handler())
	defer srv.Close()

	// A short TTL so that the second Start refreshes right away.
	r := New(Config{
		Families:  FamiliesFor("v4"),
		URL:       srv.URL,
		TTL:       1 * time.Millisecond,
		Transport: func(string) (http.RoundTripper, error) { return srv.Client().Transport, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if got := r.Address(); got != "203.0.113.7" {
		t.Fatalf("Address = %q, want the reported address", got)
	}

	// The echo breaks, then the resolver asks again.
	e.set("", http.StatusInternalServerError)
	time.Sleep(5 * time.Millisecond)
	r.refresh(ctx)

	if got := r.Address(); got != "203.0.113.7" {
		t.Fatalf("Address = %q, want the last known address to survive a failure", got)
	}
	entry := r.Addresses()[0]
	if entry.Error == "" {
		t.Fatal("the failure is not visible: the age would look fresh forever")
	}
}

// TestAnUnknownAddressIsNotAFailureToStart — the daemon runs without one.
//
// The address is a diagnostic. A daemon that refused to start because it could
// not learn its own address would trade a working gateway for an empty column.
func TestAnUnknownAddressIsNotAFailureToStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	r := New(Config{
		Families:  FamiliesFor("v4"),
		URL:       srv.URL,
		Transport: func(string) (http.RoundTripper, error) { return srv.Client().Transport, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start refused: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if got := r.Address(); got != "" {
		t.Fatalf("Address = %q, want empty", got)
	}
	entry := r.Addresses()[0]
	if entry.Error == "" || entry.Address != "" {
		t.Fatalf("entry = %+v, want an empty address with a reason", entry)
	}
}

// TestAnEchoWithoutAnAddressIsAnError — a reachable service that says nothing is
// not an answer.
//
// A 200 with nothing in it would otherwise be stored as an empty address and
// counted as a success, which is how a page noticed by the monitor would look
// healthy while measuring nothing.
func TestAnEchoWithoutAnAddressIsAnError(t *testing.T) {
	e := &echo{body: "fl=1f2\nwarp=off\n"}
	srv := httptest.NewServer(e.handler())
	defer srv.Close()

	r := New(Config{
		Families:  FamiliesFor("v4"),
		URL:       srv.URL,
		Transport: func(string) (http.RoundTripper, error) { return srv.Client().Transport, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if entry := r.Addresses()[0]; entry.Error == "" {
		t.Fatalf("entry = %+v, want a reason", entry)
	}
}

// TestATransportFailureIsReported — a family whose transport cannot be built is
// not silently skipped.
func TestATransportFailureIsReported(t *testing.T) {
	r := New(Config{
		Families: FamiliesFor("v6"),
		URL:      "http://unused.invalid/",
		Transport: func(string) (http.RoundTripper, error) {
			return nil, errors.New("no route")
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	entry := r.Addresses()[0]
	if entry.Error == "" {
		t.Fatalf("entry = %+v, want the transport failure", entry)
	}
}

// TestStopIsSafeWithoutStart — a daemon that failed to come up still stops.
func TestStopIsSafeWithoutStart(t *testing.T) {
	r := New(Config{Families: FamiliesFor("v4")})
	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestFamiliesForMapsTheAxis — one place decides what the axis means.
func TestFamiliesForMapsTheAxis(t *testing.T) {
	cases := []struct {
		axis string
		want []string
	}{
		{"v4", []string{"v4"}},
		{"v6", []string{"v6"}},
		{"auto", []string{"v4", "v6"}},
		{"", []string{"v4", "v6"}},
	}
	for _, c := range cases {
		got := FamiliesFor(c.axis)
		if len(got) != len(c.want) {
			t.Fatalf("FamiliesFor(%q) = %v, want %v", c.axis, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("FamiliesFor(%q) = %v, want %v", c.axis, got, c.want)
			}
		}
	}
}

// TestEachFamilyHasItsOwnEchoAddress — one URL for both would leave the IPv6
// answer permanently unknown.
//
// A family-pinned transport cannot dial an IPv4 literal over IPv6, so the two
// families cannot share one address literal. The failure would look like the
// network's while being the address's.
func TestEachFamilyHasItsOwnEchoAddress(t *testing.T) {
	v4, v6 := URLFor(FamilyV4), URLFor(FamilyV6)
	if v4 == v6 {
		t.Fatalf("both families use %q", v4)
	}
	if !strings.HasPrefix(v6, "https://[") {
		t.Fatalf("v6 address = %q, want a bracketed IPv6 literal", v6)
	}
	if !strings.Contains(v4, "1.1.1.1") {
		t.Fatalf("v4 address = %q, want the service's IPv4 address", v4)
	}
}
