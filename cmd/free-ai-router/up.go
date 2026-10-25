package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"free-ai-router/internal/catalog"
	"free-ai-router/internal/config"
	"free-ai-router/internal/control"
	"free-ai-router/internal/egress"
	"free-ai-router/internal/gateway"
	"free-ai-router/internal/pool"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
	"free-ai-router/internal/transport"
)

// daemon — the assembled daemon with all its parts.
type daemon struct {
	cfg       *config.Config
	providers *provider.Registry
	transport transport.Transport
	keys      *keyRegistry
	state     *state.Store
	log       *state.Logger
	// egress observes the address this host's traffic leaves from. It may be
	// nil, which means the daemon does not know its address and the counters are
	// kept without one.
	egress  *egress.Resolver
	handler http.Handler
}

// build assembles the daemon from the configuration.
//
// The order of the steps matters: each next one makes use of the previous. The
// log is opened first, because any failure further on has to be reported into
// it; the state comes before the providers, because the mechanisms write
// counters into it.
func build(cfg *config.Config) (*daemon, error) {
	d := &daemon{cfg: cfg}

	// The log. Rotation is done at open time: that is the only moment when the
	// file can be moved without racing the writers.
	logger, logErr := state.OpenLogger(cfg.Paths.LogFile)
	if logErr != nil {
		// The log is not critical: the daemon works without it too, and the
		// cause is visible on stderr. Refusing to start because of the log
		// would be disproportionate.
		fmt.Fprintf(os.Stderr, "warning: log unavailable: %v\n", logErr)
	}
	d.log = logger

	// The state.
	st, err := state.Open(cfg.Paths.StateFile)
	if err != nil {
		// A corrupted file does not block the start: the counters are for
		// reference only.
		if d.log != nil {
			d.log.Warn("state: %v", err)
		}
	}
	d.state = st

	// The providers. The log goes to the catalog refreshers: a poll failure has
	// no caller to return to, and the log is the only place it can be reported.
	var buildLog func(string, ...any)
	if d.log != nil {
		buildLog = d.log.Warn
	}
	providers, err := catalog.BuildWithLog(cfg, buildLog)
	if err != nil {
		return nil, fmt.Errorf("providers: %w", err)
	}
	d.providers = providers

	// The key rotation mechanisms. Built before the transport: the pool probes the
	// provider with the same key the requests carry, and an anonymous credential
	// must reach the probe as well.
	d.keys = newKeyRegistry(cfg, d.providers)

	tr, err := transports(d).Build(transport.ID(cfg.Transport.Val), transportSettings(cfg))
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	d.transport = tr

	// The egress address.
	//
	// The resolver is handed a way to make a transport of its own for a given
	// family: the observation has to travel the same path as the requests, or it
	// describes a different path. A failure to observe is not fatal — the
	// counters are then kept without an address.
	d.egress = egress.New(egress.Config{
		Families:  egress.FamiliesFor(cfg.Family.Val),
		Transport: d.egressTransport,
		Log:       d.log,
	})

	d.handler = d.buildHandler()
	return d, nil
}

// buildHandler assembles the surfaces into a single handler.
//
// THE TOKEN CHECK WRAPS BOTH SURFACES. This is fundamental: the models surface
// spends the operator's quota — it goes to the provider on their behalf — while
// the control surface can change keys. Either of the two being open makes server
// mode an open relay, so the check sits around the whole router, not inside one
// of the surfaces.
//
// Previously the check existed only inside the control surface, and the models
// surface stayed open: anyone who found the port could spend the quota.
func (d *daemon) buildHandler() http.Handler {
	root := http.NewServeMux()

	// The OpenAI surface.
	gw := &gateway.Handler{
		Providers:       d.providers,
		Transport:       d.transport,
		State:           d.state,
		Log:             d.log,
		Addresses:       d.egress,
		KeyRotation:     d.keys.rotationMap(),
		Retry:           retryPolicies(d.cfg, d.providers),
		DefaultProvider: d.keys.firstProvider(),
		// The credential axis reaches the request path here. Without it the
		// gateway would fall back to the pool's presence, and a pool configured
		// under "anonymous" would be spent — the very thing the axis exists to
		// prevent.
		//
		// The adapter converts the provider identifier: the configuration speaks
		// in plain strings, while the gateway identifies providers by their own
		// type, and neither package should have to learn the other's spelling.
		Credential: func(id provider.ID) string { return d.cfg.CredentialFor(string(id)) },

		// The silence budgets reach the request path here. Without them the
		// gateway has only the numbers a provider declares for itself, and a
		// provider that declares none leaves a request waiting on a connection
		// that has gone quiet — which is what the client sees as a daemon that
		// has hung.
		//
		// Where both exist the provider's numbers win: they are part of ITS
		// contract, and the pause a reasoning model holds is why they are
		// per-model at all.
		FirstEventTimeout: d.cfg.FirstEventTimeout.Val,
		IdleTimeout:       d.cfg.IdleTimeout.Val,

		// The model axis reaches the request path here. It is a decision, not a
		// safety net: with it on, an answer may come from a model the client did
		// not name, and the body says so — the default is off, and the switch
		// exists for the operator who prefers any answer to the right one's
		// absence.
		ModelFallback: d.cfg.ModelFallback.Val == config.FallbackOn,
	}
	gw.AllRoutes(root)

	// The control surface.
	ctrl := &control.Handler{
		Providers:   d.providers,
		KeyRotation: d.keys,
		State:       d.state,
		Log:         d.log,
		Config:      d.cfg,
		Transports:  transportIDs(),
		Egress:      d.egress,
		Pool:        poolOf(d.transport),
	}
	ctrl.Routes(root)

	// The access check on top of both surfaces.
	//
	// The control surface receives the "token already checked" mark so that it
	// does not check it a second time: a double check is not an error, but it
	// would mean two places where one question is decided.
	return requireToken(root, d.cfg)
}

// serve starts the daemon and runs until a stop signal.
func (d *daemon) serve(ctx context.Context) error {
	if err := d.transport.Start(ctx); err != nil {
		return fmt.Errorf("starting the transport: %w", err)
	}
	defer d.transport.Stop()

	// The egress observation starts before the listener, on purpose: the first
	// request must be attributable to an address, and an observation started
	// later would leave the first request of the session uncounted by address.
	// It cannot fail in a way that matters — an address that could not be learned
	// leaves the counters without one.
	if err := d.egress.Start(ctx); err != nil && d.log != nil {
		d.log.Warn("egress: %v", err)
	}
	defer d.egress.Stop()

	// The catalog refresh starts before the listener too, and for the same
	// reason: reads come out of memory, so a catalog that is still empty would
	// answer 404 to every request until the first poll. Start waits for that
	// first read, and a failure is not fatal — the poller keeps trying and the
	// gateway works, minus the models it cannot name.
	startPollers(ctx, d.providers)
	defer stopPollers(d.providers)

	srv := &http.Server{
		Addr:              d.cfg.Listen.Val,
		Handler:           d.handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	ln, err := net.Listen("tcp", d.cfg.Listen.Val)
	if err != nil {
		return fmt.Errorf("address %s is taken: %w", d.cfg.Listen.Val, err)
	}

	// The identifier is written AFTER the listener is bound, so a file that
	// exists always names a daemon that reached the point of serving. Writing
	// it earlier would leave a claim behind on the failure paths above, and a
	// stale claim is what makes `stop` signal an unrelated process.
	//
	// A failure here is not fatal: the daemon works without the file, and the
	// only thing lost is the ability to stop it by file. Refusing to start
	// over a bookkeeping file would trade a working gateway for a tidy
	// directory.
	if err := writePIDFile(d.cfg.Paths.PIDFile); err != nil && d.log != nil {
		d.log.Warn("cannot write the pid file %s: %v", d.cfg.Paths.PIDFile, err)
	}
	defer removePIDFile(d.cfg.Paths.PIDFile)

	if d.log != nil {
		d.log.Info("daemon started on %s, mode %s, transport %s, providers %d",
			d.cfg.Listen.Val, d.cfg.Mode.Val, d.cfg.Transport.Val, d.providers.Len())
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		// Clean shutdown: wait for the current requests to finish so as not
		// to cut streams off in the middle.
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		if d.log != nil {
			d.log.Info("daemon stopped")
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// cmdUp starts the daemon.
func cmdUp(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if f == nil {
		return nil
	}

	cfg, err := loadConfigFor(f)
	if err != nil {
		return err
	}
	f.applyPool(cfg)

	// Printing the effective configuration is useful in itself, and also as a
	// way to check the merging of the levels without starting anything.
	if f.printConfig {
		return printEffectiveConfig(cfg)
	}

	d, err := build(cfg)
	if err != nil {
		return err
	}
	defer func() {
		if d.log != nil {
			_ = d.log.Close()
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return d.serve(ctx)
}

// buildVersion is injected at build time through -ldflags.
var buildVersion string

// version reports the build version.
//
// The order matters, because the injected value is absent in builds that were
// not produced by the release recipe. A development build used to answer
// "unknown", which is exactly the case where the version is most useful: a
// failure gets reported from a binary whose provenance nobody recorded.
//
// So the injected value is preferred, and the module version embedded by the
// toolchain is the fallback. That second source needs no flags at all, and it
// is populated when the binary is built from a module (which includes
// `go install`). Only when both are empty — a plain `go build` in a working
// copy — does the result stay unknown, and then the revision suffix still
// identifies the checkout it came from.
func version() string {
	if v := buildVersion; v != "" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		// A working-copy build carries no version, but the toolchain records
		// the revision it was made from. Reporting that is what turns an
		// otherwise anonymous binary back into a traceable one.
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				if len(s.Value) > 12 {
					return "devel+" + s.Value[:12]
				}
				return "devel+" + s.Value
			}
		}
	}
	return "unknown"
}

// transportSettings returns the transport's settings with the addressing axis
// folded in.
//
// The family is a first-class axis, while the transport also carries a bag of
// keys of its own. Folding rather than replacing keeps whatever a particular
// transport needs; the family key belongs to the axis, so the axis wins — two
// places holding one value is how the value ends up disagreeing with itself.
//
// This is the whole of the plumbing: the transport has pinned the address family
// all along, and the axis was simply never reaching it.
func transportSettings(cfg *config.Config) map[string]any {
	settings := make(map[string]any, len(cfg.TransportConfig)+1)
	for k, v := range cfg.TransportConfig {
		settings[k] = v
	}
	settings["family"] = cfg.Family.Val
	return settings
}

// transportIDs returns the available transports as strings.
//
// The interfaces need strings, not typed identifiers: they build a choice out of
// them, and the typing adds nothing there.
func transportIDs() []string {
	// The pool is registered by the daemon rather than by the transport package, so
	// the list of choices is the daemon's own.
	ids := append(transport.NewRegistryWithDefaults().IDs(), pool.ID)
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// startPollers starts the providers that keep their catalog fresh themselves.
//
// Type-asserted rather than declared on Provider: a provider whose catalog is a
// constant has nothing to refresh, and requiring the method of everyone would
// put two do-nothing implementations in the codebase to satisfy an interface.
//
// A provider that fails to warm up is NOT reported here: Start does not fail, it
// leaves the catalog empty and keeps retrying. The failure is visible in the
// poller's log line instead, where the reason is.
func startPollers(ctx context.Context, reg *provider.Registry) {
	for _, p := range reg.All() {
		if polling, ok := p.(provider.Polling); ok {
			polling.Start(ctx)
		}
	}
}

// stopPollers ends the catalog refreshers.
func stopPollers(reg *provider.Registry) {
	for _, p := range reg.All() {
		if polling, ok := p.(provider.Polling); ok {
			_ = polling.Stop()
		}
	}
}

// transports assembles the transports this daemon offers.
//
// The pool is registered here rather than inside the transport package: it needs the
// provider to probe with, the key that probe should carry, a log and a place for its
// own state, and registration is the moment when the daemon's dependencies are within
// reach. The transport package stays free of providers because of it.
func transports(d *daemon) *transport.Registry {
	registry := transport.NewRegistryWithDefaults()
	registry.Register(pool.TransportInfo, func(cfg map[string]any) (transport.Transport, error) {
		return pool.New(cfg, pool.Deps{
			Provider:  d.probeProvider(),
			Key:       d.probeKey,
			Log:       poolLogger{d.log},
			StatePath: poolStatePath(d.cfg),
		})
	})
	return registry
}

// probeProvider is the provider the pool judges addresses against.
//
// The judgement is the provider's own: whether it refuses to serve this address is
// what the pool asks, and it asks the one provider whose answer it can classify. A
// daemon without that provider cannot run the pool, and says so at build time.
func (d *daemon) probeProvider() provider.Provider {
	if d.providers == nil {
		return nil
	}
	all := d.providers.All()
	for _, p := range all {
		if p.ID() == probeProviderID {
			return p
		}
	}
	if len(all) == 0 {
		return nil
	}
	return all[0]
}

// probeProviderID is the provider whose refusal the pool understands.
const probeProviderID provider.ID = "opencode"

// probeKey is the key a probe carries.
//
// The first of the provider's keys, and nothing under the anonymous credential: the
// probe must travel with the same credentials as the requests it stands for, or it
// answers about a different client.
func (d *daemon) probeKey() string {
	p := d.probeProvider()
	if p == nil {
		return ""
	}
	if d.cfg.CredentialFor(string(p.ID())) == config.CredentialAnonymous {
		return ""
	}
	keys := d.keys.Keys(p.ID())
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// poolStatePath is where the pool keeps its list between runs.
func poolStatePath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(cfg.Paths.StateFile), "pool.json")
}

// egressTransport hands the resolver the path the requests take.
//
// Under direct routing the address is observed through a transport of the family
// being measured, because the family is what the observation is about. Under any
// other transport the family axis is inert: the path is the transport's own, and
// asking for a family would describe a route that is not used.
func (d *daemon) egressTransport(family string) (http.RoundTripper, error) {
	if d.transport == nil || d.transport.ID() == transport.Direct {
		return transport.Pinned(family), nil
	}
	return d.transport.RoundTripper()
}

// poolLogger adapts the daemon's log to the pool's one method.
//
// The wrapper exists for the nil case: a daemon that could not open its log still
// runs, and the pool must not dereference a missing logger to report that it could
// not fetch a subscription.
type poolLogger struct{ log *state.Logger }

// Warn implements pool.Logger.
func (l poolLogger) Warn(format string, args ...any) {
	if l.log != nil {
		l.log.Warn(format, args...)
	}
}

// poolOf exposes the pool to the control surface, or nothing when it is not in use.
func poolOf(tr transport.Transport) control.PoolStatus {
	if p, ok := tr.(control.PoolStatus); ok {
		return p
	}
	return nil
}
