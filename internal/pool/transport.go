package pool

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"free-ai-router/internal/provider"
	"free-ai-router/internal/transport"
)

// ID is the transport identifier in configuration and in the CLI.
const ID transport.ID = "pool"

// Deps is what the pool needs from the daemon: a provider to ask, the key that
// request should carry, and a place to keep its own state.
type Deps struct {
	Provider  provider.Provider
	Key       func() string
	Log       Logger
	StatePath string
	Now       func() time.Time
}

// Transport is the pool as a transport: one round tripper that always points at the
// node currently serving.
type Transport struct {
	settings Settings
	deps     Deps
	reg      *Registry
	runner   *Runner
	core     Core
	sw       *Switcher
	eval     evaluator
	now      func() time.Time

	mu     sync.Mutex
	cancel context.CancelFunc
	// runCtx is the daemon's lifetime. A pass asked for over HTTP must not run on
	// the request's context: the answer ends that context, and the pass would die
	// right after the 202 that announced it.
	runCtx     context.Context
	wg         sync.WaitGroup
	started    bool
	nextEval   time.Time
	evalState  string
	lastReport Report
	// stateLast is the last pass as the state file recorded it: after a restart the
	// surfaces still have to show what the previous process measured.
	stateLast  *LastRun
	evaluating bool

	reevalMu sync.Mutex
}

// New builds the transport from configuration and the daemon's dependencies.
func New(cfg map[string]any, deps Deps) (transport.Transport, error) {
	settings, err := DecodeSettings(Section(cfg))
	if err != nil {
		return nil, err
	}
	core, err := coreByName(settings.Core)
	if err != nil {
		return nil, err
	}
	if deps.Provider == nil {
		return nil, fmt.Errorf("pool: no provider to probe with")
	}

	now := deps.Now
	if now == nil {
		now = time.Now
	}

	t := &Transport{
		settings:  settings,
		deps:      deps,
		reg:       NewRegistry(settings.MaxNodes, settings.DeadThreshold),
		runner:    NewRunner(settings.CorePath, ""),
		core:      core,
		now:       now,
		evalState: "never",
	}

	t.sw = NewSwitcher(SwitcherConfig{
		Registry: t.reg,
		Runner:   t.runner,
		Core:     core,
		Grace:    settings.Grace,
		Log:      deps.Log,
		Explain:  t.explain,
	})

	evalConfig := Config{
		Source:       settings.Source,
		MaxProbe:     settings.MaxProbe,
		ProbeWorkers: settings.ProbeWorkers,
		ProbeTimeout: settings.ProbeTimeout,
		MaxNodes:     settings.MaxNodes,
		ProbeModel:   settings.ProbeModel,
	}
	if settings.Source == SourceProxprobe {
		logPath := ""
		if deps.StatePath != "" {
			logPath = filepath.Join(filepath.Dir(deps.StatePath), "proxprobe.log")
		}
		pass := PassRunner{
			Command: settings.ProxprobeCommand,
			Dir:     settings.ProxprobeWorkDir,
			Timeout: settings.PassTimeout,
			LogPath: logPath,
		}
		evalConfig.LoadPass = func() (Pass, error) {
			return LoadPass(settings.ProxprobeWorkDir, settings.ProxprobeMaxAge, t.now())
		}
		// An empty command means the pass belongs to an outside scheduler: the pool
		// then only reads what that pass last produced.
		if len(settings.ProxprobeCommand) > 0 {
			evalConfig.RunPass = pass.Run
		}
	}
	t.eval = NewEvaluator(evalConfig, t.reg, t.runner, core, deps.Provider, deps.Key, deps.Log)

	// The list from the previous run serves from the first second: re-evaluating
	// takes a minute, and waiting for it would leave the proxy useless after every
	// restart.
	state, err := Load(deps.StatePath)
	if err != nil && deps.Log != nil {
		deps.Log.Warn("pool: state: %v", err)
	}
	t.reg.Restore(state.Nodes, state.Quarantined, state.Active)
	t.nextEval = state.NextEvalAt
	t.stateLast = state.Last
	if state.EvalState != "" {
		t.evalState = state.EvalState
	}
	if state.EvaluatedAt.IsZero() {
		t.evalState = "never"
	}
	return t, nil
}

// ID implements transport.Transport.
func (t *Transport) ID() transport.ID { return ID }

// RoundTripper implements transport.Transport.
func (t *Transport) RoundTripper() (http.RoundTripper, error) { return t.sw.RoundTripper() }

// TransportInfo is how the pool presents itself to the registry and the interfaces.
var TransportInfo = transport.Info{
	ID:                   ID,
	Summary:              "routes provider traffic through measured third-party servers, rotating on limits and failures",
	NeedsExternalProcess: true,
}

// Info implements transport.Transport.
func (t *Transport) Info() transport.Info { return TransportInfo }

// Start implements transport.Transport: it starts the schedule, the health loop, and
// the tunnel.
func (t *Transport) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	t.runCtx = runCtx
	t.started = true
	t.mu.Unlock()

	t.wg.Add(2)
	go func() {
		defer t.wg.Done()
		t.schedule(runCtx)
	}()
	go func() {
		defer t.wg.Done()
		t.health(runCtx)
	}()

	// Bring a tunnel up at once when the list survived the restart, and check the
	// pass at once when it did not: waiting for the scheduled moment would
	// leave the proxy answering "not ready" for hours after a start without state.
	if t.reg.Empty() {
		go t.Recheck(runCtx)
	} else {
		go t.applyList(runCtx)
	}
	return nil
}

// Stop implements transport.Transport.
func (t *Transport) Stop() error {
	t.mu.Lock()
	cancel := t.cancel
	t.cancel = nil
	t.runCtx = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	t.sw.Stop()
	t.wg.Wait()
	t.save()
	return nil
}

// evaluator is the evaluation pass, narrowed to what the transport uses.
//
// An interface rather than the concrete *Evaluator for one reason: the schedule and
// the one-pass-at-a-time rule have to be testable without a network, a core process
// or a provider request.
type evaluator interface {
	Run(ctx context.Context) Report
	Progress() (done, total int)
}

// Status is the pool's state as the surfaces show it.
type Status struct {
	Active      *View            `json:"active,omitempty"`
	Relevant    int              `json:"relevant"`
	Nodes       []View           `json:"nodes,omitempty"`
	Quarantined []QuarantineView `json:"quarantined,omitempty"`
	EvalState   string           `json:"eval_state"`
	Evaluating  bool             `json:"evaluating"`
	// Source is where the list came from: a proxprobe pass or the builtin pipeline.
	Source      string    `json:"source"`
	EvaluatedAt time.Time `json:"evaluated_at,omitempty"`
	NextEvalAt  time.Time `json:"next_eval_at"`
	Progress    Progress  `json:"progress"`
	Last        *LastRun  `json:"last,omitempty"`
	Switches    int       `json:"switches"`
	Failed      int       `json:"failed_switches"`
}

// Progress is the probe stage.
type Progress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

// LastRun is the last evaluation in the terms the interfaces speak.
type LastRun struct {
	At          time.Time `json:"at,omitempty"`
	Source      string    `json:"source,omitempty"`
	PassRan     bool      `json:"pass_ran,omitempty"`
	Parsed      int       `json:"parsed"`
	HTTPOK      int       `json:"http_ok,omitempty"`
	Unsupported int       `json:"unsupported,omitempty"`
	Probed      int       `json:"probed"`
	Accepted    int       `json:"accepted"`
	Error       string    `json:"error,omitempty"`
	DurationMs  int64     `json:"duration_ms"`
}

// summary turns a report into the shape the surfaces and the state file carry.
func (r Report) summary() *LastRun {
	return &LastRun{
		At:          r.Started,
		Source:      r.Source,
		PassRan:     r.PassRan,
		Parsed:      r.Parsed,
		HTTPOK:      r.HTTPOK,
		Unsupported: r.Unsupported,
		Probed:      r.Probed,
		Accepted:    r.Accepted,
		Error:       errorText(r.Err),
		DurationMs:  durationMs(r.Started, r.Finished),
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Status reports everything the surfaces need.
func (t *Transport) Status() Status {
	t.mu.Lock()
	evalState := t.evalState
	next := t.nextEval
	rep := t.lastReport
	evaluating := t.evaluating
	t.mu.Unlock()

	done, total := t.eval.Progress()
	switches, failed := t.sw.Stats()
	raw := t.reg.Snapshot()

	out := Status{
		Relevant:   raw.Relevant,
		EvalState:  evalState,
		Evaluating: evaluating,
		Source:     t.settings.Source,
		NextEvalAt: next,
		Progress:   Progress{Done: done, Total: total},
		Switches:   switches,
		Failed:     failed,
	}
	found := false
	for _, n := range raw.Nodes {
		out.Nodes = append(out.Nodes, NodeView(n))
		if n.ID == raw.Active {
			v := NodeView(n)
			out.Active = &v
			found = true
		}
	}
	// A serving node can leave the list and keep serving: an evaluation re-measures,
	// and a server that answers real traffic is not dropped because a probe could not
	// place one through it (its address may have spent the allowance the probe needs).
	// The switcher is what knows; the list only knows what was ranked.
	if !found {
		if n, ok := t.sw.Active(); ok {
			v := NodeView(n)
			out.Active = &v
		}
	}
	for _, e := range raw.Quarantined {
		out.Quarantined = append(out.Quarantined, QuarantineView{Node: NodeView(e.Node), Reason: e.Reason, Since: e.Since})
	}
	if !rep.Started.IsZero() {
		out.EvaluatedAt = rep.Started
		out.Last = rep.summary()
	} else if t.stateLast != nil {
		out.Last = t.stateLast
		out.EvaluatedAt = t.stateLast.At
	}
	return out
}

// Recheck asks for an evaluation now, at most once per reeval_min_gap.
//
// It exists for the empty pool: the schedule is a fixed set of instants, and waiting
// for the next one while nothing serves is not an option. Whether a pass is already
// running is decided by the pass itself, so a scheduled one and a requested one
// cannot overlap. The caller's context is not the pass's: the HTTP request ends when
// the 202 is written, and a pass tied to it would be cancelled before it started.
func (t *Transport) Recheck(ctx context.Context) {
	t.mu.Lock()
	if t.evaluating || (!t.lastReport.Started.IsZero() && t.now().Sub(t.lastReport.Finished) < t.settings.ReevalMinGap) {
		t.mu.Unlock()
		return
	}
	if t.runCtx != nil {
		ctx = t.runCtx
	}
	t.mu.Unlock()

	go t.evaluate(ctx)
}

// schedule runs the evaluation at the fixed instants of the day.
func (t *Transport) schedule(ctx context.Context) {
	for {
		next := t.nextRun()
		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		if !sleepCtx(ctx, wait) {
			return
		}
		t.evaluate(ctx)
	}
}

// nextRun returns the instant of the next evaluation and remembers it.
func (t *Transport) nextRun() time.Time {
	now := t.now()
	first := t.settings.FirstEvaluation(now)

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.nextEval.IsZero() || !t.nextEval.After(now) {
		t.nextEval = NextEvaluation(now, first, t.settings.Interval)
	}
	return t.nextEval
}

// evaluate runs one full pass. A failure is recorded, not raised: there is no caller
// to raise it to.
//
// The pass claims itself rather than trusting its callers: the schedule, the health
// loop and the control surface all reach this function, and two passes at once would
// run two sets of cores against one quota and race on the list.
func (t *Transport) evaluate(ctx context.Context) {
	t.mu.Lock()
	if t.evaluating {
		t.mu.Unlock()
		return
	}
	t.evaluating = true
	t.evalState = "evaluating"
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.evaluating = false
		t.mu.Unlock()
	}()

	report := t.eval.Run(ctx)

	t.mu.Lock()
	t.lastReport = report
	if report.Err != nil {
		t.evalState = "error"
	} else {
		t.evalState = "ready"
	}
	t.nextEval = NextEvaluation(t.now(), t.settings.FirstEvaluation(t.now()), t.settings.Interval)
	t.mu.Unlock()

	if t.deps.Log != nil {
		// The middle count is what the pass itself proved: its own http stage, not this
		// pipeline's.
		proven := fmt.Sprintf("%d passed the pass", report.HTTPOK)
		if report.Err != nil {
			t.deps.Log.Warn("pool: evaluation failed: %v (%d parsed, %s, %d probed)", report.Err, report.Parsed, proven, report.Probed)
		} else {
			t.deps.Log.Warn("pool: %d servers relevant (%d parsed, %s, %d probed) in %s", report.Accepted, report.Parsed, proven, report.Probed, report.Finished.Sub(report.Started).Round(time.Second))
		}
	}
	t.save()
	if report.Err == nil {
		t.applyList(ctx)
	}
}

// applyList points the switcher at the list, keeping a serving node that survived.
func (t *Transport) applyList(ctx context.Context) {
	if t.reg.Empty() {
		return
	}
	if _, ok := t.reg.Active(); ok {
		if t.sw.HealthCheck(ctx) {
			return
		}
	}
	if err := t.sw.Switch(ctx); err != nil && t.deps.Log != nil {
		t.deps.Log.Warn("pool: %v", err)
	}
}

// health watches the serving core and the list.
func (t *Transport) health(ctx context.Context) {
	interval := t.settings.HealthInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if t.reg.Empty() {
				t.Recheck(ctx)
				continue
			}
			if !t.sw.HealthCheck(ctx) {
				t.Recheck(ctx)
			}
		}
	}
}

// save writes the list and the schedule to disk.
func (t *Transport) save() {
	if t.deps.StatePath == "" {
		return
	}
	t.mu.Lock()
	state := State{
		Version:     StateVersion,
		EvaluatedAt: t.lastReport.Started,
		NextEvalAt:  t.nextEval,
		EvalState:   t.evalState,
		Source:      t.settings.Source,
	}
	if !t.lastReport.Started.IsZero() {
		state.Last = t.lastReport.summary()
	} else {
		state.Last = t.stateLast
	}
	t.mu.Unlock()

	snapshot := t.reg.Snapshot()
	state.Nodes = snapshot.Nodes
	state.Active = snapshot.Active
	state.Quarantined = snapshot.Quarantined
	if err := Save(t.deps.StatePath, state); err != nil && t.deps.Log != nil {
		t.deps.Log.Warn("pool: saving state: %v", err)
	}
}

// explain is the sentence the client sees while nothing is ready: it says what is
// happening and when it will be known, because "not ready" alone reads as a broken
// proxy.
func (t *Transport) explain() string {
	snap := t.Status()
	if !snap.EvaluatedAt.IsZero() && snap.Relevant == 0 {
		if snap.Last.Error != "" {
			return fmt.Sprintf("the relevant list is empty: %s; next check at %s", snap.Last.Error, snap.NextEvalAt.UTC().Format(time.RFC3339))
		}
	}
	base := "the pool has no usable server yet"
	if snap.Progress.Total > 0 {
		base = fmt.Sprintf("%s: %d of %d servers tested", base, snap.Progress.Done, snap.Progress.Total)
	}
	// An empty list is retried off the schedule as well, so the next scheduled
	// check is not the wait the caller is about to sit through.
	if snap.Relevant == 0 && t.settings.ReevalMinGap > 0 {
		return fmt.Sprintf("%s; another check runs within %s", base, t.settings.ReevalMinGap)
	}
	if !snap.NextEvalAt.IsZero() {
		if wait := time.Until(snap.NextEvalAt); wait > 0 {
			return fmt.Sprintf("%s; next check in %s", base, wait.Round(time.Second))
		}
		return fmt.Sprintf("%s; checking now", base)
	}
	return base
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// durationMs is how long a run took, zero when it did not finish.
func durationMs(started, finished time.Time) int64 {
	if started.IsZero() || finished.IsZero() || finished.Before(started) {
		return 0
	}
	return finished.Sub(started).Milliseconds()
}
