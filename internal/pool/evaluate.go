package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"free-ai-router/internal/provider"
)

// Report is the outcome of one evaluation. It exists because the pipeline has four
// stages and the interesting question about a failure is WHICH stage lost the
// servers.
type Report struct {
	Started  time.Time
	Finished time.Time
	// Source names where the candidates came from, so a report read after a
	// restart is not mistaken for another source's.
	Source      string
	PassRan     bool
	Parsed      int
	Unsupported int
	HTTPOK      int
	Probed      int
	Accepted    int
	Err         error
}

// Config is what the pipeline needs to know.
type Config struct {
	Source       string
	MaxProbe     int
	ProbeWorkers int
	ProbeTimeout time.Duration
	MaxNodes     int
	ProbeModel   string
	// RunPass and LoadPass are the proxprobe source. Run is the pass itself, absent
	// when an outside scheduler owns it; Load reads the pass's artifacts.
	RunPass  func(ctx context.Context) error
	LoadPass func() (Pass, error)
}

// Logger is the minimum the pool reports through.
type Logger interface {
	Warn(string, ...any)
}

// Evaluator runs the pipeline: read the pass, admit what it kept, rank and install.
//
// The stages are strictly ordered because each one is much more expensive than the
// previous: reading a file costs nothing, a core costs a process, and a completion
// costs the provider's quota. Stopping early is the whole design.
type Evaluator struct {
	cfg      Config
	reg      *Registry
	runner   *Runner
	core     Core
	provider provider.Provider
	key      func() string
	log      Logger
	now      func() time.Time

	pass        func() (Pass, error)
	probeTunnel func(ctx context.Context, t *Tunnel, model provider.Model) Verdict

	done  atomic.Int64
	total atomic.Int64
}

// NewEvaluator wires the pipeline.
func NewEvaluator(cfg Config, reg *Registry, runner *Runner, core Core, p provider.Provider, key func() string, log Logger) *Evaluator {
	e := &Evaluator{
		cfg:      cfg,
		reg:      reg,
		runner:   runner,
		core:     core,
		provider: p,
		key:      key,
		log:      log,
		now:      time.Now,
	}
	e.probeTunnel = e.probeOne
	return e
}

// Progress reports how far the probe stage has gone, for the interfaces.
func (e *Evaluator) Progress() (done, total int) {
	return int(e.done.Load()), int(e.total.Load())
}

// Run executes the whole pipeline and installs the result.
//
// A run that fails at any stage leaves the current list untouched: an evaluation
// that saw nothing is not evidence that nothing works.
// The result is named so that the deferred stamp below reaches the caller: a value
// returned by a defer that writes to a copy would leave Finished at zero, and every
// duration reported from it would be nonsense.
func (e *Evaluator) Run(ctx context.Context) (rep Report) {
	rep = Report{Started: e.now(), Source: e.cfg.Source}
	e.done.Store(0)
	e.total.Store(0)
	defer func() {
		rep.Finished = e.now()
		e.done.Store(0)
		e.total.Store(0)
	}()

	return e.runProxprobe(ctx, rep)
}

// runProxprobe evaluates from a finished proxprobe pass.
//
// Nothing is fetched, parsed, sorted or pinged here: the pass owns that, and its
// artifacts are the contract. What remains is the one thing the pass cannot answer —
// whether the provider accepts a real request through the server — plus the exit
// address that the allowance is counted against.
func (e *Evaluator) runProxprobe(ctx context.Context, rep Report) Report {
	if e.cfg.RunPass != nil {
		rep.PassRan = true
		if err := e.cfg.RunPass(ctx); err != nil {
			rep.Err = err
			return rep
		}
	}
	if e.cfg.LoadPass == nil {
		rep.Err = errors.New("the pool has no proxprobe pass to read")
		return rep
	}

	pass, err := e.cfg.LoadPass()
	if err != nil {
		rep.Err = err
		return rep
	}
	rep.Parsed = pass.Counts.Parsed
	rep.HTTPOK = pass.Counts.HTTPOK
	rep.Unsupported = pass.Counts.Unsupported
	if len(pass.Nodes) == 0 {
		rep.Err = fmt.Errorf("proxprobe left no usable server: %d parsed, %d passed its http check, %d unrenderable",
			pass.Counts.Parsed, pass.Counts.HTTPOK, pass.Counts.Unsupported)
		return rep
	}

	model := e.model(ctx)
	if model.ID == "" {
		rep.Err = errors.New("the provider has no model to probe with: the catalog is not ready")
		return rep
	}

	candidates := pass.Nodes
	if e.cfg.MaxProbe > 0 && len(candidates) > e.cfg.MaxProbe {
		candidates = candidates[:e.cfg.MaxProbe]
	}

	e.total.Store(int64(len(candidates)))
	accepted, probed, reasons := e.probeAll(ctx, candidates, model)
	rep.Probed = probed
	rep.Accepted = len(accepted)
	if len(accepted) == 0 {
		rep.Err = fmt.Errorf("no server was accepted by the provider: %s", dominant(reasons, probed))
		return rep
	}

	e.reg.Replace(accepted)
	return rep
}

// dominant names the reason most servers were rejected for.
func dominant(reasons map[string]int, probed int) string {
	best, count := "", 0
	for reason, n := range reasons {
		if n > count {
			best, count = reason, n
		}
	}
	if best == "" || count == 0 {
		return "no verdict was produced"
	}
	return fmt.Sprintf("%s (%d of %d)", best, count, probed)
}

// probeAll opens one core per candidate, asks the provider, and closes it again.
//
// The parallelism is bounded by the probe workers, not by the candidate count: each
// worker is a process plus an outbound request, and six of those is what the host
// was measured to sustain.
func (e *Evaluator) probeAll(ctx context.Context, candidates []Node, model provider.Model) ([]Node, int, map[string]int) {
	workers := e.cfg.ProbeWorkers
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)

	var (
		mu       sync.Mutex
		accepted []Node
		reasons  = map[string]int{}
		// spent holds the exit pools an address refusal has already burned this
		// run: a refusal is about the pool, so the rest of the pool is not worth a
		// core process, and a node accepted before the refusal cannot serve either.
		spent  = map[string]string{}
		probed atomic.Int64
		wg     sync.WaitGroup
	)

	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(n Node) {
			defer wg.Done()
			defer func() { <-sem }()

			defer func() {
				e.done.Add(1)
				probed.Add(1)
			}()

			t, err := e.runner.Start(ctx, e.core, n)
			if err != nil {
				if e.log != nil {
					e.log.Warn("pool: %s did not start a core: %v", n.Label, err)
				}
				return
			}
			defer t.Stop(0)

			verdict := e.probeTunnel(ctx, t, model)
			if !verdict.Accepted {
				if e.log != nil {
					// The reason is the operator's only handle on a list that came out
					// empty or short: one line per rejected server says whether the
					// address was refused, the tunnel went silent, or the node only
					// looked alive from here.
					e.log.Warn("pool: %s rejected: %s (%s)", n.Label, verdict.Reason, verdict.ExitIP)
				}
				mu.Lock()
				reasons[verdict.Reason]++
				if verdict.AddressRefused && verdict.ExitIP != "" {
					// A refusal is about the exit network, not this one server: mark
					// the pool spent and drop what it already put in the list.
					if p := exitPrefix(verdict.ExitIP); p != "" {
						spent[p] = verdict.ExitIP
						kept := accepted[:0]
						for _, a := range accepted {
							if !sameExitPool(a.ExitIP, verdict.ExitIP) {
								kept = append(kept, a)
								continue
							}
							reasons["its exit pool is spent"]++
							if e.log != nil {
								e.log.Warn("pool: %s dropped: another server in its exit network was refused", a.Label)
							}
						}
						accepted = kept
					}
				}
				mu.Unlock()
				return
			}
			n.ProbeRTT = verdict.RTT
			n.ExitIP = verdict.ExitIP
			mu.Lock()
			if verdict.ExitIP != "" && spent[exitPrefix(verdict.ExitIP)] != "" {
				// Accepted before the refusal and served after it: the allowance is
				// the pool's, so this node is already worthless.
				reasons["its exit pool is spent"]++
				if e.log != nil {
					e.log.Warn("pool: %s dropped: another server in its exit network was refused", n.Label)
				}
				mu.Unlock()
				return
			}
			accepted = append(accepted, n)
			mu.Unlock()
		}(candidate)
	}
	wg.Wait()
	return accepted, int(probed.Load()), reasons
}

// probeOne builds the request the way a client would and asks.
func (e *Evaluator) probeOne(ctx context.Context, t *Tunnel, model provider.Model) Verdict {
	prober := Prober{
		Provider: e.provider,
		Model:    model,
		Key:      e.key,
		Timeout:  e.cfg.ProbeTimeout,
	}
	return prober.Run(ctx, t)
}

// catalogWait bounds how long a run waits for the provider's catalog to warm up.
// The transport starts before the catalog refresher does, so the first evaluation of
// a session can legitimately find the catalog empty; a run without a model cannot
// shape a request the lane would admit, and starting cores for it only wastes them.
const catalogWait = 30 * time.Second

// model resolves the model the probe spends. An empty setting means the first free
// model of the provider, which is what a client would get by default.
func (e *Evaluator) model(ctx context.Context) provider.Model {
	deadline := time.Now().Add(catalogWait)
	for {
		model, err := e.lookupModel(ctx)
		if model.ID != "" || ctx.Err() != nil {
			return model
		}
		if time.Now().After(deadline) {
			if e.log != nil {
				e.log.Warn("pool: the probe has no model: %v", err)
			}
			return provider.Model{}
		}
		select {
		case <-ctx.Done():
			return provider.Model{}
		case <-time.After(time.Second):
		}
	}
}

// lookupModel asks the provider's catalog once.
func (e *Evaluator) lookupModel(ctx context.Context) (provider.Model, error) {
	if e.provider == nil {
		return provider.Model{}, errors.New("no provider")
	}
	catalogCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	models, err := e.provider.Models(catalogCtx)
	if err != nil {
		return provider.Model{}, fmt.Errorf("model catalog: %w", err)
	}
	if e.cfg.ProbeModel != "" {
		for _, m := range models {
			if m.ID == e.cfg.ProbeModel {
				return m, nil
			}
		}
		if e.log != nil {
			e.log.Warn("pool: probe model %q is not in the catalog, using the first free one", e.cfg.ProbeModel)
		}
	}
	for _, m := range models {
		if m.Free {
			return m, nil
		}
	}
	if len(models) > 0 {
		return models[0], nil
	}
	return provider.Model{}, errors.New("the catalog is empty")
}
