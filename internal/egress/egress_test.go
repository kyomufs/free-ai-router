package egress

import (
	"context"
	"errors"
	"fmt"
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
	r.refresh(ctx, false)

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

// clock is a clock the test moves by hand, so that a TTL and a retry interval can
// be outlived without sleeping through them.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// TestAnUnansweredFamilyIsRetriedWithinTheTTL — the wait for an unknown address
// is bounded by Retry, not by the TTL.
//
// WHY THIS IS NOT COSMETIC. A request sent while the address is unknown is counted
// in the day and in the model, and dropped from the address axis, because an empty
// address deliberately creates no entry. So waiting out a TTL is not a stale
// column: it is missing rows, and the breakdown stops adding up to the day it
// belongs to. A family that never resolved is exactly the case a TTL cannot
// govern — there is no answer for a TTL to expire.
func TestAnUnansweredFamilyIsRetriedWithinTheTTL(t *testing.T) {
	e := &echo{status: http.StatusInternalServerError}
	srv := httptest.NewServer(e.handler())
	defer srv.Close()

	c := &clock{at: time.Now()}
	r := New(Config{
		Families:  FamiliesFor("v4"),
		URL:       srv.URL,
		Retry:     time.Minute,
		Now:       c.now,
		Transport: func(string) (http.RoundTripper, error) { return srv.Client().Transport, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if got := r.Address(); got != "" {
		t.Fatalf("Address = %q, want unknown after a failing echo", got)
	}

	// A recovery is not looked for before Retry has passed: the echo answers
	// nothing, and asking it every tick would be a loop, not a retry.
	e.set("ip=203.0.113.7\n", 0)
	c.advance(30 * time.Second)
	r.refresh(ctx, false)
	if n := len(e.asked()); n != 1 {
		t.Fatalf("the echo was asked %d times, want 1: a failure is throttled by Retry", n)
	}

	// Once Retry has passed the address is learned, while the TTL has not.
	c.advance(40 * time.Second)
	r.refresh(ctx, false)
	if got := r.Address(); got != "203.0.113.7" {
		t.Fatalf("Address = %q, want the recovered answer without waiting out the TTL", got)
	}
}

// TestATickThatLandsAMomentEarlyStillAsks — the throttle and the tick that feeds
// it are the same interval.
//
// A tick that arrives a few milliseconds short of the interval used to be
// skipped, which put the next attempt a whole interval away: observed live as a
// family retried every two and three minutes instead of every one, with the
// address unknown the whole time — and an unknown address is a day of counters
// that do not add up.
func TestATickThatLandsAMomentEarlyStillAsks(t *testing.T) {
	e := &echo{status: http.StatusInternalServerError}
	srv := httptest.NewServer(e.handler())
	defer srv.Close()

	c := &clock{at: time.Now()}
	const retry = time.Minute
	r := New(Config{
		Families:  FamiliesFor("v4"),
		URL:       srv.URL,
		Retry:     retry,
		Now:       c.now,
		Transport: func(string) (http.RoundTripper, error) { return srv.Client().Transport, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	if n := len(e.asked()); n != 1 {
		t.Fatalf("the echo was asked %d times at the start, want 1", n)
	}

	// Well before the interval the failure is still throttled: a retry is not a
	// loop.
	c.advance(retry / 2)
	r.refresh(ctx, false)
	if n := len(e.asked()); n != 1 {
		t.Fatalf("the echo was asked %d times, want a failure throttled by Retry", n)
	}

	// Inside the tolerance it is not: the tick has come, and answering it costs
	// one request per interval either way.
	c.advance(retry/2 - retryTolerance/2)
	r.refresh(ctx, false)
	if n := len(e.asked()); n != 2 {
		t.Fatalf("a tick inside the tolerance was skipped: the echo was asked %d times, want 2", n)
	}
}

// TestRefreshDoesNotWaitOutTheTTL — the address can be re-asked on demand.
//
// This is the path a refusal takes. The operator moves to another uplink because
// the current one is spent, and the requests that follow have to be credited to
// the address they really leave from: an answer up to a TTL old credits them to
// the address the operator has just left, which is the counter that must not keep
// growing.
func TestRefreshDoesNotWaitOutTheTTL(t *testing.T) {
	e := &echo{body: "ip=203.0.113.7\n"}
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

	// The uplink changed. The cached answer is still the one from before.
	e.set("ip=198.51.100.9\n", 0)

	// The ordinary refresh honours the TTL: the answer is fresh, so it is kept.
	r.refresh(ctx, false)
	if got := r.Address(); got != "203.0.113.7" {
		t.Fatalf("Address = %q, want a fresh answer to be trusted", got)
	}

	// The on-demand one does not.
	r.Refresh(ctx)
	if got := r.Address(); got != "198.51.100.9" {
		t.Fatalf("Address = %q, want the changed address to be taken at once", got)
	}
}

// TestTheAddressIsTrustedForAMinute — how long an answer is trusted is how long a
// switched uplink keeps being credited to the address it left.
//
// A policy, not an implementation detail: the address is what the free tier is
// counted against, and every request in that window is filed under the wrong one.
func TestTheAddressIsTrustedForAMinute(t *testing.T) {
	if DefaultTTL > time.Minute {
		t.Fatalf("DefaultTTL = %s, want at most a minute", DefaultTTL)
	}
}

// warnLog records the warnings the resolver reports.
type warnLog struct {
	mu       sync.Mutex
	warnings []string
}

func (l *warnLog) Warn(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func (l *warnLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warnings...)
}

// TestAnAddressChangeIsReportedOnce — the journal has to say that the address
// became a different one, and only when it did.
//
// The monitor shows the address it has now; the one thing it cannot show is that
// this is a DIFFERENT address from the one an hour ago, and that line is what
// explains a counter row the operator did not expect.
func TestAnAddressChangeIsReportedOnce(t *testing.T) {
	e := &echo{body: "ip=203.0.113.7\n"}
	srv := httptest.NewServer(e.handler())
	defer srv.Close()

	log := &warnLog{}
	c := &clock{at: time.Now()}
	r := New(Config{
		Families:  FamiliesFor("v4"),
		URL:       srv.URL,
		Log:       log,
		Now:       c.now,
		Transport: func(string) (http.RoundTripper, error) { return srv.Client().Transport, nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Stop() }()

	// The first observation is not a change: there was no address before it.
	if got := log.all(); len(got) != 0 {
		t.Fatalf("warnings = %v, want none for a first observation", got)
	}

	// The uplink is switched, and the answer is asked for again once its
	// lifetime has passed.
	e.set("ip=198.51.100.9\n", 0)
	c.advance(2 * time.Minute)
	r.refresh(ctx, false)

	got := log.all()
	if len(got) != 1 {
		t.Fatalf("warnings = %v, want one report of the change", got)
	}
	if want := "address changed from 203.0.113.7 to 198.51.100.9"; !strings.Contains(got[0], want) {
		t.Fatalf("warning = %q, want it to contain %q", got[0], want)
	}

	// The same address seen again is not a change, and a report repeated on
	// every refresh would bury it in the journal it is meant to explain.
	r.Refresh(ctx)
	if n := len(log.all()); n != 1 {
		t.Fatalf("the change was reported %d times, want once", n)
	}
}
