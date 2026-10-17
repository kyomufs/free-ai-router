package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"free-ai-router/internal/catalog"
	"free-ai-router/internal/config"
	"free-ai-router/internal/control"
	"free-ai-router/internal/gateway"
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
	handler   http.Handler
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

	// The providers.
	providers, err := catalog.Build(cfg)
	if err != nil {
		return nil, fmt.Errorf("providers: %w", err)
	}
	d.providers = providers

	// The transport.
	tr, err := transport.NewRegistryWithDefaults().Build(transport.ID(cfg.Transport.Val), cfg.TransportConfig)
	if err != nil {
		return nil, fmt.Errorf("transport: %w", err)
	}
	d.transport = tr

	// The key rotation mechanisms.
	d.keys = newKeyRegistry(cfg, d.providers)

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
		KeyRotation:     d.keys.rotationMap(),
		DefaultProvider: d.keys.firstProvider(),
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

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	f.applyToConfig(cfg)

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

// transportIDs returns the available transports as strings.
//
// The interfaces need strings, not typed identifiers: they build a choice out of
// them, and the typing adds nothing there.
func transportIDs() []string {
	ids := transport.NewRegistryWithDefaults().IDs()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}
