package pool

import (
	"context"
	"sync"
	"testing"
	"time"
)

// slowEvaluator is a pass that blocks until released, so a second pass has
// somewhere to be refused.
type slowEvaluator struct {
	mu      sync.Mutex
	runs    int
	started chan struct{}
	release chan struct{}
}

func newSlowEvaluator() *slowEvaluator {
	return &slowEvaluator{started: make(chan struct{}, 8), release: make(chan struct{})}
}

func (e *slowEvaluator) Run(ctx context.Context) Report {
	e.mu.Lock()
	e.runs++
	e.mu.Unlock()
	e.started <- struct{}{}
	<-e.release
	return Report{Started: time.Now(), Finished: time.Now()}
}

func (e *slowEvaluator) Progress() (int, int) { return 0, 0 }

func (e *slowEvaluator) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs
}

// One pass at a time, whichever caller arrives.
//
// The schedule, the health loop and the control surface all reach the pass, and two
// at once would run two sets of cores against one quota and race on the list. Before
// this was claimed inside the pass, only Recheck knew a pass was running, so a
// scheduled pass was invisible to the health loop.
func TestTransportRunsOneEvaluationAtATime(t *testing.T) {
	e := newSlowEvaluator()
	tr := &Transport{
		eval:      e,
		reg:       NewRegistry(4, 2),
		settings:  Settings{},
		now:       time.Now,
		evalState: "never",
	}

	tr.Recheck(context.Background())
	select {
	case <-e.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the pass did not start")
	}

	// The two other callers arrive while it runs: the schedule calls the pass
	// directly, the health loop asks for a check.
	tr.evaluate(context.Background())
	tr.Recheck(context.Background())

	// The refused callers return at once, so the count is final here: one.
	if got := e.count(); got != 1 {
		t.Fatalf("passes served while one ran: %d, want 1", got)
	}

	close(e.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		running := tr.evaluating
		tr.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	tr.mu.Lock()
	running, state := tr.evaluating, tr.evalState
	tr.mu.Unlock()
	if running {
		t.Fatal("the pass is still marked as running after it returned")
	}
	if state != "ready" {
		t.Fatalf("eval_state %q, want ready", state)
	}
}

// A server that leaves the list keeps serving, and the surfaces must still name it.
//
// The probe spends the allowance it measures, so a node accepted at 03:00 can be
// refused at 11:00 while real traffic still flows through it. Then the list no longer
// holds it, and a status built from the list alone would report nothing serving.
func TestStatusNamesTheServingNodeAfterItLeavesTheList(t *testing.T) {
	serving := Node{ID: "b", Label: "b", ExitIP: "2.2.2.2"}
	other := Node{ID: "a", Label: "a", ExitIP: "1.1.1.1"}

	reg := NewRegistry(10, 1)
	reg.Replace([]Node{serving, other})

	sw := NewSwitcher(SwitcherConfig{Registry: reg, Runner: NewRunner("/nonexistent", ""), Core: fakeCoreImpl{}})
	sw.cur.Store(&Tunnel{Node: serving})

	tr := &Transport{eval: newSlowEvaluator(), reg: reg, sw: sw, now: time.Now}

	reg.Replace([]Node{other})

	st := tr.Status()
	if st.Active == nil {
		t.Fatal("no serving node named while the switcher serves one")
	}
	if st.Active.ID != serving.ID {
		t.Fatalf("active %q, want %q", st.Active.ID, serving.ID)
	}
	if st.Relevant != 1 {
		t.Fatalf("relevant %d, want 1", st.Relevant)
	}
	for _, n := range st.Nodes {
		if n.ID == serving.ID {
			t.Fatal("the dropped node came back into the ranked list")
		}
	}
}

// ctxEvaluator records the context its pass was given, after the caller released it.
type ctxEvaluator struct {
	seen    chan error
	release chan struct{}
}

func (e *ctxEvaluator) Run(ctx context.Context) Report {
	<-e.release
	e.seen <- ctx.Err()
	return Report{}
}

func (e *ctxEvaluator) Progress() (int, int) { return 0, 0 }

// TestRecheckOutlivesTheRequestThatAskedForIt: the 202 ends the HTTP request, and
// with it the request's context. A pass that inherited it would be cancelled before
// it probed anything, and the operator would read "accepted" over an unchanged
// report.
func TestRecheckOutlivesTheRequestThatAskedForIt(t *testing.T) {
	e := &ctxEvaluator{seen: make(chan error, 1), release: make(chan struct{})}
	tr := &Transport{eval: e, reg: NewRegistry(4, 2), now: time.Now, evalState: "never"}
	runCtx, stop := context.WithCancel(context.Background())
	defer stop()
	tr.runCtx = runCtx

	reqCtx, reqDone := context.WithCancel(context.Background())
	tr.Recheck(reqCtx)
	reqDone()
	close(e.release)

	if err := <-e.seen; err != nil {
		t.Fatalf("the pass inherited the request's cancellation: %v", err)
	}
}
