// Package egress answers WHERE the daemon's traffic leaves from.
//
// WHY THIS EXISTS. The provider's anonymous tier is limited per address, not per
// key: the client sends no credentials, and the service identifies the caller by
// the address the request arrives from. A refusal then says "this address has had
// its share", and no key in the pool changes that. Without knowing the address,
// the monitor can only show a total, and the total cannot tell a spent address
// from a spent key.
//
// WHY NOT DNS. The obvious source is a TXT record on a public resolver, which is
// what "who am I" services exist for. It is not used here, and the reason is
// measured rather than theoretical: on a host behind a transparent proxy the
// resolver is a local interceptor, and the answer is the interceptor's own
// placeholder address (a 198.18.0.0/15 address, in the case examined) rather than
// the address the internet sees. A name that is dead makes this worse, not
// better: a failure and a wrong answer arrive in the same shape.
//
// WHAT IS USED INSTEAD. An HTTPS echo address is asked over the SAME transport and
// the same address family as a model request. That is the whole point: the answer
// is guaranteed to describe the path the requests actually take, because it is
// the same path. A resolver that asked over a different route would answer a
// different question, and the difference is exactly what the monitor exists to
// show.
//
// The cost is a network round trip, which is why the answer is cached and
// refreshed in the background rather than requested per model call.
package egress

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Family names. The same values as the gateway's addressing axis, and the same
// two buckets the provider's limiter keeps apart.
const (
	FamilyV4 = "v4"
	FamilyV6 = "v6"
)

// DefaultURL is the echo address.
//
// An address in Cloudflare's own range rather than a third-party "what is my IP"
// service: it is reachable over both families, it answers in a fixed key=value
// form that does not need parsing heuristics, and it is part of a large CDN's
// public diagnostics rather than a hobby endpoint that may disappear. The `ip=`
// line is the address, as seen by the edge.
const DefaultURL = "https://1.1.1.1/cdn-cgi/trace"

// DefaultURLV6 is the same document over the service's IPv6 address.
//
// WHY IT IS A SEPARATE ADDRESS. The host name in DefaultURL is an IPv4 literal,
// and a family-pinned transport cannot reach an IPv4 literal over IPv6: the dial
// fails with "no suitable address found" before a single byte leaves. One URL
// for both families would therefore leave the IPv6 answer permanently unknown,
// even on a host whose IPv6 works — the failure would look like the network's
// while being the address's.
//
// The service publishes both addresses under one certificate, so the literal form
// validates without a custom root.
const DefaultURLV6 = "https://[2606:4700:4700::1111]/cdn-cgi/trace"

// DefaultTTL is how long an answer is trusted.
//
// A MINUTE, and the cost of it is one small request per family per minute on a
// connection that is already warm. What it buys is the case that made this a
// complaint: the operator switches the uplink by hand — a VPN, a tethered
// modem — and from that moment every request leaves from an address the resolver
// still believes is the old one. The counters are then attributed to an address
// that is no longer in use, which is worse than a missing address: the free tier
// is limited per address, so the monitor's rows answer the operator's real
// question ("which address spent the quota") with the wrong address.
//
// Five minutes was the earlier value, on the grounds that the address rarely
// changes. It rarely changes per hour and often changes per session — a switch is
// exactly the moment the operator is watching, and it is the moment the answer
// has to be right.
const DefaultTTL = time.Minute

// DefaultRetry is how soon a family that has no answer is asked again.
//
// The TTL cannot govern this. A TTL says how long a KNOWN answer is trusted, and
// a family that never resolved has nothing to trust; gating both on it is what
// left an address unknown for the whole five minutes between two ticks. While the
// address is unknown every request is counted without one, and the monitor's rows
// then do not add up to the day they belong to.
//
// A minute rather than seconds: the echo is a third party, and a family that is
// permanently unusable — an IPv6 route the uplink does not have — must not be
// asked to death for an answer it can never give.
const DefaultRetry = time.Minute

// retryTolerance is how much of the retry interval the throttle gives up.
//
// The throttle and the tick that feeds it are the same interval, and a tick that
// arrives a few milliseconds before the throttle expires is skipped — the next
// one is then a whole interval away. Observed live as a family retried every two
// and three minutes instead of every one. The intent is "no more often than
// Retry", and one second of slack keeps it while removing the double wait.
const retryTolerance = time.Second

// DefaultTimeout bounds one echo request.
const DefaultTimeout = 10 * time.Second

// FamiliesFor turns the addressing axis into the families to observe.
//
// The mapping lives here rather than at the call site because the axis value and
// the families are one decision: "auto" is exactly "ask both", and a caller that
// spelled out the list itself would be a second place to change when the axis
// gains a value.
//
// An unknown value falls back to both. The axis is validated before the daemon is
// built, so this is not a configuration path; falling back to the broader answer
// keeps the monitor useful rather than empty if it is ever reached.
func FamiliesFor(axis string) []string {
	switch axis {
	case FamilyV4:
		return []string{FamilyV4}
	case FamilyV6:
		return []string{FamilyV6}
	default:
		// IPv4 first: the order decides the primary address, and an address that
		// is reachable regardless of the uplink's IPv6 state is the more useful
		// default.
		return []string{FamilyV4, FamilyV6}
	}
}

// URLFor returns the echo address for a family.
//
// The two families are two literals of the same service rather than one name
// resolved twice, because the transport is pinned before the name is resolved:
// pinning is what makes the answer describe the family being asked about.
func URLFor(family string) string {
	if family == FamilyV6 {
		return DefaultURLV6
	}
	return DefaultURL
}

// TransportFunc returns a transport for an address family.
//
// A function rather than an import of the transport package: the resolver must
// use whatever transport the daemon was configured with, and taking the package
// as a dependency would let it drift to its own choice. An empty family means
// "let the system decide".
type TransportFunc func(family string) (http.RoundTripper, error)

// Config is what the resolver needs to work.
type Config struct {
	// Families are the address families to resolve, in order of preference.
	//
	// The order decides the primary answer: the first family that resolves is
	// the one reported by Address. Empty means both, IPv4 first — an address
	// that can always be reached is a more useful default than one that depends
	// on the uplink having a working IPv6 route.
	Families []string
	// TTL is how long an answer is trusted.
	TTL time.Duration
	// Retry is how soon a family that returned no answer is asked again. Zero
	// means DefaultRetry.
	Retry time.Duration
	// Timeout bounds one request.
	Timeout time.Duration
	// Transport returns the transport for a family. Required.
	Transport TransportFunc
	// URL overrides the echo address for every family. Empty means the service's
	// own address for the family is used, which is what a deployment wants; a
	// test sets it to a local server.
	URL string
	// Log receives warnings. May be nil.
	Log Logger
	// Now is the clock. May be nil, which means the system clock.
	Now func() time.Time
}

// Logger is the part of the logging interface this package uses.
type Logger interface {
	Warn(format string, args ...any)
}

// closer is a transport that can drop its idle connections.
type closer interface {
	CloseIdleConnections()
}

// Resolver resolves and caches the egress address per family.
type Resolver struct {
	cfg Config

	mu        sync.Mutex
	answers   map[string]answer
	primary   string
	started   bool
	stop      chan struct{}
	stopped   chan struct{}
	refreshMu sync.Mutex
	// rts holds one transport per family, built once. Building it per request
	// would mean paying a fresh TLS handshake for every observation, and the
	// pool it leaves behind would never be closed.
	rts map[string]http.RoundTripper
}

// answer is what one family resolved to, plus how it went.
type answer struct {
	// address is the observed egress address, empty when unknown.
	address string
	// at is when it was observed.
	at time.Time
	// attempt is when it was last ASKED for, whether or not the answer arrived.
	// Kept apart from at so that a failure can be throttled without pretending
	// that a fresh address was seen.
	attempt time.Time
	// err is why it is unknown, if it is.
	err error
}

// Address is one family's answer, for interfaces.
type Address struct {
	// Family is the family the address belongs to.
	Family string `json:"family"`
	// Address is the observed egress address. Empty when it could not be
	// determined.
	Address string `json:"address,omitempty"`
	// ResolvedAt is when the address was observed, in milliseconds since the
	// epoch. Zero when it never was.
	ResolvedAt int64 `json:"resolved_at,omitempty"`
	// AgeMs is how old the answer is.
	AgeMs int64 `json:"age_ms,omitempty"`
	// Error is why the address is unknown, if it is.
	Error string `json:"error,omitempty"`
}

// New creates a resolver. It does not resolve anything: Start does.
func New(cfg Config) *Resolver {
	// An empty URL is not filled in here: it means "the address of the family
	// being asked", which is decided per family in resolve.
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.Retry <= 0 {
		cfg.Retry = DefaultRetry
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if len(cfg.Families) == 0 {
		cfg.Families = []string{FamilyV4, FamilyV6}
	}

	return &Resolver{
		cfg:     cfg,
		answers: make(map[string]answer, len(cfg.Families)),
		rts:     make(map[string]http.RoundTripper, len(cfg.Families)),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

// Start resolves once and then keeps the answer fresh in the background.
//
// The first resolution happens here rather than on the first request: a request
// that had to wait for a round trip to a third party would be slower than the
// model call it is counting, and a failed resolution would turn a countable
// request into an uncountable one.
//
// A resolution that fails is not an error from Start. The address is optional
// bookkeeping: without it requests still run, they are simply not attributed to
// an address, and refusing to start over it would take the daemon down for a
// diagnostic.
func (r *Resolver) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return nil
	}
	r.started = true
	r.mu.Unlock()

	r.prepareTransports()
	r.refresh(ctx, false)

	go r.loop(ctx)
	return nil
}

// prepareTransports builds one transport per family.
//
// A family whose transport cannot be built is not an error here: the transport
// axis already refuses to start the daemon when its own constructor fails, so a
// failure at this point is a bug in the wiring rather than a configuration
// mistake, and it is reported through the per-family error like any other.
func (r *Resolver) prepareTransports() {
	if r.cfg.Transport == nil {
		return
	}
	for _, family := range r.cfg.Families {
		rt, err := r.cfg.Transport(family)
		if err != nil {
			if r.cfg.Log != nil {
				r.cfg.Log.Warn("egress: family %s: transport: %v", family, err)
			}
			continue
		}
		r.rts[family] = rt
	}
}

// loop keeps the answers fresh.
//
// The tick is the RETRY interval, not the TTL, and that is deliberate: the tick
// only DECIDES whether a family is due, and refresh does the deciding. A tick
// that follows the TTL can only ever retry an unanswered family once per TTL,
// which is what made an address that failed to resolve stay unknown long after
// the echo recovered.
func (r *Resolver) loop(ctx context.Context) {
	defer close(r.stopped)

	ticker := time.NewTicker(r.cfg.Retry)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			r.refresh(ctx, false)
		}
	}
}

// Stop ends the background refresh.
func (r *Resolver) Stop() error {
	r.mu.Lock()
	started := r.started
	r.started = false
	r.mu.Unlock()

	if started {
		close(r.stop)
		<-r.stopped
	}

	// The observation transports are the resolver's own, so it is the resolver
	// that lets go of their idle connections. Without this the daemon would
	// leave two pools of connections to a third party open until the process
	// exits.
	for _, rt := range r.rts {
		if c, ok := rt.(closer); ok {
			c.CloseIdleConnections()
		}
	}
	return nil
}

// Address returns the primary egress address, or an empty string when it is not
// known. It implements the gateway's resolver interface.
//
// The primary address is the first family that resolved, in the configured order.
// It is not a guess about which family a request will use: it is the first one
// that could be observed, and the per-family answers are what the monitor shows.
func (r *Resolver) Address() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.primary
}

// Addresses returns the answers by family.
func (r *Resolver) Addresses() []Address {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.cfg.Now()
	out := make([]Address, 0, len(r.cfg.Families))
	for _, family := range r.cfg.Families {
		a := r.answers[family]
		item := Address{Family: family, Address: a.address}
		if !a.at.IsZero() {
			item.ResolvedAt = a.at.UnixMilli()
			item.AgeMs = now.Sub(a.at).Milliseconds()
		}
		if a.err != nil {
			item.Error = a.err.Error()
		}
		out = append(out, item)
	}
	return out
}

// Refresh re-resolves now, ignoring the TTL of an answer that is already known.
//
// It exists because the address is not only a column: the provider's free tier is
// limited per address, so a refusal is the moment the address stops being
// bookkeeping and becomes the question. An uplink that changed a minute ago is
// not noticed by the tick until the TTL expires, and every request in between is
// attributed to the address the traffic no longer leaves from.
//
// A family whose last attempt failed is still throttled by Retry: a refusal is
// not a reason to hammer an echo that is not answering.
func (r *Resolver) Refresh(ctx context.Context) { r.refresh(ctx, true) }

// refresh resolves every configured family, skipping the ones whose answer is
// still fresh. A forced refresh re-asks a family that already has an answer.
func (r *Resolver) refresh(ctx context.Context, force bool) {
	// One resolution at a time. The background tick and an on-demand refresh can
	// otherwise overlap, and two in-flight answers for one family would race to
	// be stored.
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	now := r.cfg.Now()
	for _, family := range r.cfg.Families {
		r.mu.Lock()
		a := r.answers[family]
		r.mu.Unlock()

		if !force && a.address != "" && now.Sub(a.at) < r.cfg.TTL {
			continue
		}

		// The last attempt failed, and the answer is unknown or stale precisely
		// because of it. Asking again right away would turn an unreachable echo
		// into a busy loop. The throttle keeps a second of slack so a tick that
		// lands a moment early is not skipped for a whole interval.
		if a.err != nil && now.Sub(a.attempt) < r.cfg.Retry-retryTolerance {
			continue
		}

		address, err := r.resolve(ctx, family)

		r.mu.Lock()
		if err != nil {
			// A failed refresh keeps the last known address: an address that was
			// observed a minute ago is still a better description of the path
			// than no address at all, and dropping it would empty the monitor's
			// rows on a single dropped packet. The error is recorded so that the
			// age and the reason stay visible.
			prev := r.answers[family]
			prev.err = err
			prev.attempt = now
			r.answers[family] = prev
			r.primary = r.primaryLocked()
			r.mu.Unlock()

			if r.cfg.Log != nil {
				r.cfg.Log.Warn("egress: family %s: %v", family, err)
			}
			continue
		}

		r.answers[family] = answer{address: address, at: now, attempt: now}
		r.primary = r.primaryLocked()
		r.mu.Unlock()

		// THE CHANGE IS THE EVENT, not the address. A monitor shows the address
		// it has now; the journal has to say that it became a different one, when,
		// and from what — that line is what explains a counter row the operator
		// did not expect, and without it an uplink switch is only inferable from
		// two rows that both look current.
		if prev := a.address; prev != "" && prev != address && r.cfg.Log != nil {
			r.cfg.Log.Warn("egress: family %s: address changed from %s to %s", family, prev, address)
		}
	}
}

// primaryLocked picks the reported address: the first family with an answer.
func (r *Resolver) primaryLocked() string {
	for _, family := range r.cfg.Families {
		if a := r.answers[family]; a.address != "" {
			return a.address
		}
	}
	return ""
}

// resolve asks the echo address over one family's transport.
func (r *Resolver) resolve(ctx context.Context, family string) (string, error) {
	rt, ok := r.rts[family]
	if !ok {
		return "", fmt.Errorf("no transport for %s", family)
	}

	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	// The address depends on the family: a family-pinned transport cannot reach
	// an IPv4 literal over IPv6, so one URL for both would leave the IPv6 answer
	// unknown on every host. An override is used as given, for tests.
	url := r.cfg.URL
	if url == "" {
		url = URLFor(family)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// A diagnostic, not a model call: the caching layer and any intermediary must
	// pass it through, or the answer describes the intermediary.
	req.Header.Set("cache-control", "no-store")

	client := &http.Client{
		Transport: rt,
		// No redirects: the address that answered is the address we asked.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("echo answered %s", resp.Status)
	}

	// The document is small and the format is line-oriented, so it is read
	// through a scanner rather than buffered whole. The `ip=` line is what is
	// wanted; everything else in it is diagnostics for a human.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if address, ok := strings.CutPrefix(line, "ip="); ok {
			address = strings.TrimSpace(address)
			if address == "" {
				return "", fmt.Errorf("echo answered with an empty address")
			}
			return address, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading the echo: %w", err)
	}
	return "", fmt.Errorf("the echo carried no address")
}
